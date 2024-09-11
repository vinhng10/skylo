package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

type Event struct {
	VehiclePlate string `json:"vehicle_plate"`
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
}

func main() {
	conn, err := amqp.Dial("amqp://guest:guest@172.17.0.3:5672/")
	failOnError(err, "Failed to connect to RabbitMQ")
	defer conn.Close()

	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	defer ch.Close()

	err = ch.ExchangeDeclare(
		"vehile", // name
		"direct", // type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
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

	router := gin.Default()
	router.POST("/entry", func(c *gin.Context) { postEvent(c, ch, entryQ.Name) })
	router.POST("/exit", func(c *gin.Context) { postEvent(c, ch, exitQ.Name) })

	router.Run("localhost:8080")
}

func postEvent(c *gin.Context, ch *amqp.Channel, qName string) {
	var newEvent Event
	if err := c.BindJSON(&newEvent); err != nil {
		return
	}
	datetimeKey := fmt.Sprintf("%s_date_time", qName)
	timedEvent := gin.H{
		"id":            uuid.NewString(),
		"vehicle_plate": newEvent.VehiclePlate,
		datetimeKey:     time.Now().Unix(),
	}

	body, _ := json.Marshal(timedEvent)

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
	if err != nil {
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": "Failed to publish a message"})
		return
	}

	c.IndentedJSON(http.StatusCreated, timedEvent)
}
