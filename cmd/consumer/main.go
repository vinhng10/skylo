package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	url := "http://172.17.0.2:8000/"

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

func processEntry(msgs <-chan amqp.Delivery, rdb *redis.Client) {
	for msg := range msgs {
		var event Event
		err := json.Unmarshal(msg.Body, &event)
		if err != nil {
			log.Printf("Failed to unmarshal exit event: %v", err)
			continue
		}

		err = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = rdb.Set(ctx, event.VehiclePlate, event.DateTime, 0).Result()
			return err
		}()
		if err != nil {
			log.Printf("Failed to set event to redis: %v", err)
			continue
		}

		log.Printf("Entry %s", event.VehiclePlate)
		msg.Ack(false)
	}
}

func processExit(msgs <-chan amqp.Delivery, rdb *redis.Client) {
	for msg := range msgs {
		var event Event
		err := json.Unmarshal(msg.Body, &event)
		if err != nil {
			log.Printf("Failed to unmarshal exit event: %v", err)
			continue
		}

		val, err := func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			val, err := rdb.Get(ctx, event.VehiclePlate).Result()
			return val, err
		}()
		if err != nil {
			log.Printf("Failed to get event from redis: %v", err)
			continue
		}

		entryDateTime, err := time.Parse(time.RFC3339, val)
		if err != nil {
			log.Printf("Failed to parse time: %v", err)
			continue
		}

		_, err = sendInvoice(Invoice{
			VehiclePlate:  event.VehiclePlate,
			EntryDateTime: entryDateTime,
			ExitDateTime:  event.DateTime,
		})
		if err != nil {
			log.Printf("Failed to send invoice: %v", err)
			continue
		}

		err = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := rdb.Del(ctx, event.VehiclePlate).Result()
			return err
		}()
		if err != nil {
			log.Printf("Failed to remove event from redis: %v", err)
			continue
		}

		log.Printf("Exit %s", event.VehiclePlate)
		msg.Ack(false)
	}
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

	exitMsgs, err := declareAndConsumeQueue(ch, "exit", "vehicle")
	failOnError(err, "Failed to register exit consumer")

	go processEntry(entryMsgs, rdb)
	go processExit(exitMsgs, rdb)

	// Keep the main function running to receive messages
	select {}
}
