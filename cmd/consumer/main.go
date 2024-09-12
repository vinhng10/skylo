package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

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

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "172.17.0.4:6379",
		Password: "", // no password set
		DB:       1,  // use default DB
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

	entryMsgs, err := declareAndConsumeQueue(ch, "entry", "vehicle")
	failOnError(err, "Failed to register entry consumer")

	go func() {
		for msg := range entryMsgs {
			func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				var event Event
				err := json.Unmarshal(msg.Body, &event)
				failOnError(err, "Failed to unmarshal entry event")

				val, err := rdb.Set(ctx, event.VehiclePlate, event.DateTime, 0).Result()
				failOnError(err, "Failed to store event in redis")

				log.Printf(" [x] %s", val)
				msg.Ack(false)
			}()
		}
	}()

	exitMsgs, err := declareAndConsumeQueue(ch, "exit", "vehicle")
	failOnError(err, "Failed to register exit consumer")

	go func() {
		for msg := range exitMsgs {
			func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				var event Event
				err := json.Unmarshal(msg.Body, &event)
				failOnError(err, "Failed to unmarshal exit event")

				val, err := rdb.GetDel(ctx, event.VehiclePlate).Result()
				if err != nil {
					log.Printf(" [x] Con Cac")
				}

				log.Printf(" [x] %s", val)
				msg.Ack(false)
			}()
		}
	}()

	log.Printf(" [*] Waiting for logs. To exit press CTRL+C")

	// Keep the main function running to receive messages
	select {}
}
