package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

type Event struct {
	ID           string    `json:"id"`
	VehiclePlate string    `json:"vehicle_plate"`
	Stage        string    `json:"stage"`
	DateTime     time.Time `json:"date_time"`
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
}

func wait() {
	wait, err := strconv.ParseInt(os.Getenv("MAX_WAIT"), 10, 64)
	if err != nil {
		wait = 1000
	}
	time.Sleep(time.Duration(rand.Intn(int(wait))+1) * time.Millisecond)
}

func timer(job string, histogram prometheus.Histogram) func() {
	start := time.Now()

	return func() {
		// Measure the elapsed time and observe it in the histogram
		histogram.Observe(time.Since(start).Seconds())

		// Push the histogram data to the Prometheus Pushgateway
		if err := push.New(fmt.Sprintf(
			"http://%s:%s",
			os.Getenv("PUSHGATEWAY_HOST"),
			os.Getenv("PUSHGATEWAY_PORT"),
		), job).
			Collector(histogram).
			Push(); err != nil {
			log.Printf("Could not push to Pushgateway: %v", err)
		}
	}
}

func generateRandomVehiclePlate() string {
	// Generate 3 random uppercase letters
	letters := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	letterPart := make([]byte, 3)
	for i := range letterPart {
		letterPart[i] = letters[rand.Intn(len(letters))]
	}

	// Generate a random 4-digit number
	numberPart := rand.Intn(10000)

	return fmt.Sprintf("%s-%04d", letterPart, numberPart)
}

func sendEvent(ch *amqp.Channel, vehiclePlate string, qName string) error {
	newEvent := Event{
		ID:           uuid.NewString(),
		VehiclePlate: vehiclePlate,
		Stage:        qName,
		DateTime:     time.Now().UTC(),
	}

	body, _ := json.Marshal(newEvent)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := ch.PublishWithContext(ctx,
		"vehicle", // exchange
		qName,     // routing key
		false,     // mandatory
		false,     // immediate
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "application/json",
			Body:         body,
		})

	return err
}

func produceEntry(ch *amqp.Channel, rdb *redis.Client, qName string) {
	// Create a Prometheus Histogram to track the duration
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "produce_entry",
		Help: "Time taken to produce vehicle entry",
	})

	for {
		func() {
			// Push time metric to Prometheus PushGateway
			defer timer("produce_entry", duration)()

			vehiclePlate := generateRandomVehiclePlate()

			exists, err := func() (int64, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				exists, err := rdb.Exists(ctx, vehiclePlate).Result()
				return exists, err
			}()
			if err != nil {
				log.Printf("Failed to check vehicle plate existence: %v\n", err)
				return
			}

			if exists == 0 {
				err := func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, err := rdb.Set(ctx, vehiclePlate, 1, 0).Result()
					return err
				}()
				if err != nil {
					log.Printf("Failed to set vehicle plate to redis: %v\n", err)
					return
				}

				err = sendEvent(ch, vehiclePlate, qName)
				if err != nil {
					log.Printf("Failed to send event: %v\n", err)
					return
				}
			} else {
				log.Printf("Vehicle plate already exists: %s", vehiclePlate)
			}
		}()
		wait()
	}
}

func produceExit(ch *amqp.Channel, rdb *redis.Client, qName string) {
	// Create a Prometheus Histogram to track the duration
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "produce_exit",
		Help: "Time taken to produce vehicle exit",
	})

	for {
		func() {
			// Push time metric to Prometheus PushGateway
			defer timer("produce_exit", duration)()

			var vehiclePlate string
			var err error

			if rand.Float32() < 0.8 {
				vehiclePlate, err = func() (string, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()

					vehiclePlate, err := rdb.RandomKey(ctx).Result()
					return vehiclePlate, err
				}()
				if err != nil {
					return
				}

				err := func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()

					_, err := rdb.Del(ctx, vehiclePlate).Result()
					return err
				}()
				if err != nil {
					log.Printf("Failed to delete vehicle plate in redis: %v\n", err)
					return
				}
			} else {
				vehiclePlate = generateRandomVehiclePlate()
			}

			if vehiclePlate != "" {
				err = sendEvent(ch, vehiclePlate, qName)
				if err != nil {
					log.Printf("Failed to send event: %v\n", err)
					return
				}
			} else {
				log.Printf("No vehicle")
			}
		}()

		wait()
	}
}

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf(
			"%s:%s",
			os.Getenv("REDIS_HOST"),
			os.Getenv("REDIS_PORT"),
		),
		Password: "", // no password set
		DB:       0,  // use default DB
	})
	defer rdb.Close()

	time.Sleep(time.Duration(30) * time.Second)
	conn, err := amqp.Dial(fmt.Sprintf(
		"amqp://guest:guest@%s:%s/",
		os.Getenv("RABBITMQ_HOST"),
		os.Getenv("RABBITMQ_PORT"),
	))
	failOnError(err, "Failed to connect to RabbitMQ")
	defer conn.Close()

	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	defer ch.Close()

	err = ch.ExchangeDeclare(
		"vehicle", // name
		"direct",  // type
		true,      // durable
		false,     // auto-deleted
		false,     // internal
		false,     // no-wait
		nil,       // arguments
	)
	failOnError(err, "Failed to declare an exchange")

	entryQ, err := ch.QueueDeclare(
		"entry", // name
		true,    // durable
		false,   // delete when unused
		false,   // exclusive
		false,   // no-wait
		nil,     // arguments
	)
	failOnError(err, "Failed to declare entry queue")

	exitQ, err := ch.QueueDeclare(
		"exit", // name
		true,   // durable
		false,  // delete when unused
		false,  // exclusive
		false,  // no-wait
		nil,    // arguments
	)
	failOnError(err, "Failed to declare exit queue")

	go produceEntry(ch, rdb, entryQ.Name)
	go produceExit(ch, rdb, exitQ.Name)

	// Keep the main function running to receive messages
	select {}
}
