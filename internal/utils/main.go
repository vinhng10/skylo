package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	amqp "github.com/rabbitmq/amqp091-go"
)

type Event struct {
	ID           string    `json:"id"`
	VehiclePlate string    `json:"vehicle_plate"`
	Stage        string    `json:"stage"`
	DateTime     time.Time `json:"date_time"`
}

func FailOnError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
}

func Timer(job string, histogram prometheus.Histogram) func() {
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

func SendEvent(ch *amqp.Channel, event Event, qName string) error {
	body, _ := json.Marshal(event)

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
