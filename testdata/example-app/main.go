package main

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/orders/{id}", handleOrder).Methods("GET")
	r.HandleFunc("/orders", handleCreate).Methods("POST")

	logrus.WithFields(logrus.Fields{"addr": ":8080"}).Info("listening")
	if err := http.ListenAndServe(":8080", r); err != nil {
		logrus.WithError(err).Fatal("server stopped")
	}
}

func handleOrder(w http.ResponseWriter, req *http.Request) {
	id := mux.Vars(req)["id"]
	logrus.Infof("fetching order %s", id)
	w.WriteHeader(http.StatusOK)
}

func handleCreate(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusCreated)
}
