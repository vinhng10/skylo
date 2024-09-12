package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"
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

func generateEntry(ch *amqp.Channel, rdb *redis.Client, qName string) {
	for {
		vehiclePlate := generateRandomVehiclePlate()

		exists, err := func() (int64, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			exists, err := rdb.Exists(ctx, vehiclePlate).Result()
			return exists, err
		}()
		if err != nil {
			log.Printf("Failed to check vehicle plate existence: %v\n", err)
			continue
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
				continue
			}

			err = sendEvent(ch, vehiclePlate, qName)
			if err != nil {
				log.Printf("Failed to send event: %v\n", err)
				continue
			}
		} else {
			log.Printf("Vehicle plate already exists: %s", vehiclePlate)
		}

		waitTime := rand.Intn(10) + 1
		time.Sleep(time.Duration(waitTime) * time.Second)
	}
}

func generateExit(ch *amqp.Channel, rdb *redis.Client, qName string) {
	for {
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
				continue
			}
		} else {
			vehiclePlate = generateRandomVehiclePlate()
		}

		if vehiclePlate != "" {
			err = sendEvent(ch, vehiclePlate, qName)
			if err != nil {
				log.Printf("Failed to send event: %v\n", err)
				continue
			}

			err := func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				_, err := rdb.Del(ctx, vehiclePlate).Result()
				return err
			}()
			if err != nil {
				log.Printf("Failed to delete vehicle plate in redis: %v\n", err)
				continue
			}
		} else {
			log.Printf("No vehicle")
		}

		waitTime := rand.Intn(10) + 1
		time.Sleep(time.Duration(waitTime) * time.Second)
	}
}

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "172.17.0.4:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})
	defer rdb.Close()

	conn, err := amqp.Dial("amqp://guest:guest@172.17.0.3:5672/")
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

	go generateEntry(ch, rdb, entryQ.Name)
	go generateExit(ch, rdb, exitQ.Name)

	// Keep the main function running to receive messages
	select {}
}
