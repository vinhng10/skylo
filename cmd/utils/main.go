package utils

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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

func Timer(stage string, histogram *prometheus.HistogramVec) func() {
	start := time.Now()

	return func() {
		// Measure the elapsed time and observe it in the histogram
		histogram.WithLabelValues(stage).Observe(float64(time.Since(start).Milliseconds()))
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
