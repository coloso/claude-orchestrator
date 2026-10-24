package main

import (
	"log"
	"net/http"
	"os"

	"orch-test/backend/internal/server"
)

func main() {
	addr := ":8000"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	h, err := server.New()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, h))
}
