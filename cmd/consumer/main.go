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

	"github.com/vinhng10/skylo/cmd/utils"

	"github.com/prometheus/client_golang/prometheus"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

type Invoice struct {
	VehiclePlate  string    `json:"vehicle_plate"`
	EntryDateTime time.Time `json:"entry_date_time"`
	ExitDateTime  time.Time `json:"exit_date_time"`
}

func hammingDistance(str1, str2 string) (int, error) {
	if len(str1) != len(str2) {
		return 0, fmt.Errorf("strings must be of the same length")
	}
	distance := 0
	for i := range str1 {
		if str1[i] != str2[i] {
			distance++
		}
	}
	return distance, nil
}

func declareAndConsumeQueue(
	ch *amqp.Channel,
	qName string,
	exName string,
) (<-chan amqp.Delivery, error) {
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
			defer utils.Timer("consume_entry", duration)()

			var event utils.Event
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

func consumeExit(msgs <-chan amqp.Delivery, rdb *redis.Client, ch *amqp.Channel) {
	// Create a Prometheus Histogram to track the duration
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "consume_exit",
		Help: "Time taken to consume vehicle exit",
	})

	for msg := range msgs {
		func() {
			// Push time metric to Prometheus PushGateway
			defer utils.Timer("consume_exit", duration)()

			var event utils.Event
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
				if err == redis.Nil {
					event.Stage = "unrecognized"
					err = utils.SendEvent(ch, event, "unrecognized")
					if err != nil {
						log.Printf("Failed to send unrecognized event: %v\n", err)
					}
					msg.Ack(false)
				} else {
					log.Printf("Failed to get event from redis: %v", err)
				}
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

func consumeUnrecognized(msgs <-chan amqp.Delivery, rdb *redis.Client) {
	// Create a Prometheus Histogram to track the duration
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "consume_unrecognized",
		Help: "Time taken to consume unrecognized vehicle",
	})

	for msg := range msgs {
		func() {
			// Push time metric to Prometheus PushGateway
			defer utils.Timer("consume_unrecognized", duration)()

			var event utils.Event
			err := json.Unmarshal(msg.Body, &event)
			if err != nil {
				log.Printf("Failed to unmarshal unrecognized event: %v", err)
				return
			}

			// Scan all vehicle plates to find most similar with Hamming distance:
			probablePlate, val, err := func() (string, string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				var cursor uint64 = 0
				probablePlate := ""
				minDistance := 8

				for {
					keys, cursor, err := rdb.Scan(ctx, cursor, "*", 100).Result()
					if err != nil {
						return "", "", err
					}

					for _, key := range keys {
						distance, err := hammingDistance(event.VehiclePlate, key)
						if err != nil {
							return "", "", err
						}

						if distance < minDistance {
							minDistance = distance
							probablePlate = key
						}
					}

					if cursor == 0 {
						break
					}
				}

				val, err := rdb.Get(ctx, probablePlate).Result()
				return probablePlate, val, err
			}()
			if err != nil {
				log.Printf("Failed to scan matching plate from redis: %v", err)
				return
			}

			entryDateTime, err := time.Parse(time.RFC3339, val)
			if err != nil {
				log.Printf("Failed to parse time: %v", err)
				return
			}

			_, err = sendInvoice(Invoice{
				VehiclePlate:  probablePlate,
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
				_, err := rdb.Del(ctx, probablePlate).Result()
				return err
			}()
			if err != nil {
				log.Printf("Failed to remove event from redis: %v", err)
				return
			}

			log.Printf("Unrecognized %s %s", event.VehiclePlate, probablePlate)
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
	utils.FailOnError(err, "Failed to connect to RabbitMQ")
	defer conn.Close()

	ch, err := conn.Channel()
	utils.FailOnError(err, "Failed to open a channel")
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
	utils.FailOnError(err, "Failed to declare an exchange")

	entryMsgs, err := declareAndConsumeQueue(ch, "entry", "vehicle")
	utils.FailOnError(err, "Failed to register entry consumer")

	exitMsgs, err := declareAndConsumeQueue(ch, "exit", "vehicle")
	utils.FailOnError(err, "Failed to register exit consumer")

	unrecognizedMsgs, err := declareAndConsumeQueue(ch, "unrecognized", "vehicle")
	utils.FailOnError(err, "Failed to register unrecognized consumer")

	go consumeEntry(entryMsgs, rdb)
	go consumeExit(exitMsgs, rdb, ch)
	go consumeUnrecognized(unrecognizedMsgs, rdb)

	// Keep the main function running to receive messages
	select {}
}
