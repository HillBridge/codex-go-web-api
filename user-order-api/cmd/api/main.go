package main

import (
	"log"
	"net/http"
)

func main() {
	server := &http.Server{
		Addr:    ":8888",
		Handler: newServer(),
	}

	log.Println("user-order-api listening on http://localhost:8888")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
