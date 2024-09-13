package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

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

type Invoice struct {
	VehiclePlate  string    `json:"vehicle_plate"`
	EntryDateTime time.Time `json:"entry_date_time"`
	ExitDateTime  time.Time `json:"exit_date_time"`
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
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

func declareAndConsumeQueue(ch *amqp.Channel, qName string, exName string) (<-chan amqp.Delivery, error) {
	// Declare the queue
	q, err := ch.QueueDeclare(
		qName, // name
		true,  // durable
		false, // delete when unused
		false, // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		return nil, err
	}

	// Bind the queue
	err = ch.QueueBind(
		q.Name, // queue name
		qName,  // routing key
		exName, // exchange
		false,  // no-wait
		nil,    // args
	)
	if err != nil {
		return nil, err
	}

	// Consume messages from the queue
	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		false,  // auto ack
		false,  // exclusive
		false,  // no local
		false,  // no wait
		nil,    // args
	)
	if err != nil {
		return nil, err
	}

	return msgs, nil
}

func sendInvoice(invoice Invoice) (*http.Response, error) {
	url := fmt.Sprintf("http://%s:%s/", os.Getenv("INVOICE_HOST"), os.Getenv("INVOICE_PORT"))

	body, err := json.Marshal(invoice)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON body: %v", err)
	}

	resp, err := http.Post(url, "application/json; charset=utf-8", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to POST invoice: %v", err)
	}
	defer resp.Body.Close()

	// Check if the response status is successful (status code 2xx)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unsuccessful response: %s", resp.Status)
	}

	return resp, nil
}

func consumeEntry(msgs <-chan amqp.Delivery, rdb *redis.Client) {
	// Create a Prometheus Histogram to track the duration
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "consume_entry",
		Help: "Time taken to consume vehicle entry",
	})

	for msg := range msgs {
		func() {
			// Push time metric to Prometheus PushGateway
			defer timer("consume_entry", duration)()

			var event Event
			err := json.Unmarshal(msg.Body, &event)
			if err != nil {
				log.Printf("Failed to unmarshal exit event: %v", err)
				return
			}

			err = func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err = rdb.Set(ctx, event.VehiclePlate, event.DateTime, 0).Result()
				return err
			}()
			if err != nil {
				log.Printf("Failed to set event to redis: %v", err)
				return
			}

			log.Printf("Entry %s", event.VehiclePlate)
			msg.Ack(false)
		}()
	}
}

func consumeExit(msgs <-chan amqp.Delivery, rdb *redis.Client) {
	// Create a Prometheus Histogram to track the duration
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "consume_exit",
		Help: "Time taken to consume vehicle exit",
	})

	for msg := range msgs {
		func() {
			// Push time metric to Prometheus PushGateway
			defer timer("consume_exit", duration)()

			var event Event
			err := json.Unmarshal(msg.Body, &event)
			if err != nil {
				log.Printf("Failed to unmarshal exit event: %v", err)
				return
			}

			val, err := func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				val, err := rdb.Get(ctx, event.VehiclePlate).Result()
				return val, err
			}()
			if err != nil {
				log.Printf("Failed to get event from redis: %v", err)
				return
			}

			entryDateTime, err := time.Parse(time.RFC3339, val)
			if err != nil {
				log.Printf("Failed to parse time: %v", err)
				return
			}

			_, err = sendInvoice(Invoice{
				VehiclePlate:  event.VehiclePlate,
				EntryDateTime: entryDateTime,
				ExitDateTime:  event.DateTime,
			})
			if err != nil {
				log.Printf("Failed to send invoice: %v", err)
				return
			}

			err = func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err := rdb.Del(ctx, event.VehiclePlate).Result()
				return err
			}()
			if err != nil {
				log.Printf("Failed to remove event from redis: %v", err)
				return
			}

			log.Printf("Exit %s", event.VehiclePlate)
			msg.Ack(false)
		}()
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
		DB:       1,  // use default DB
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

	entryMsgs, err := declareAndConsumeQueue(ch, "entry", "vehicle")
	failOnError(err, "Failed to register entry consumer")

	exitMsgs, err := declareAndConsumeQueue(ch, "exit", "vehicle")
	failOnError(err, "Failed to register exit consumer")

	go consumeEntry(entryMsgs, rdb)
	go consumeExit(exitMsgs, rdb)

	// Keep the main function running to receive messages
	select {}
}
