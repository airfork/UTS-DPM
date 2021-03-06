package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	_ "github.com/heroku/x/hmetrics/onload"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

var tpl = template.Must(template.ParseGlob("views/*.gohtml"))
var store = sessions.NewCookieStore(
	[]byte(os.Getenv("SESSION_KEY")),
	[]byte(os.Getenv("ENCRYPTION_KEY")))
var production, csrfProtection bool

func init() {
	// Sets all cookies stored in this cookie store to have these values
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400, // Max age of one day
		HttpOnly: true,
		Secure:   production,
	}
	// Check if code is in production
	if os.Getenv("PRODUCTION") != "" {
		production = true
	}
	if os.Getenv("NOCSRF") == "" {
		csrfProtection = true
	} 
}

func main() {
	r := mux.NewRouter()
	c := newController(getSession(), store, tpl)
	// Creates some timeout rules for connections
	// Using the regular http.ListenAndServe does not set any timeout values, and this is a bad thing
	srv := http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		Addr:         ":" + os.Getenv("PORT"),
		Handler:      csrf.Protect([]byte(os.Getenv("CSRF_KEY")), csrf.Secure(csrfProtection))(r),
	}
	r.HandleFunc("/", c.index)
	r.HandleFunc("/approve", c.renderApprovals).Methods("GET")
	r.HandleFunc("/change", c.changePass).Methods("POST", "GET")
	r.HandleFunc("/data", c.dataPage).Methods("GET")
	r.HandleFunc("/data/dpms", c.getDPMXLSX).Methods("GET")
	r.HandleFunc("/data/users", c.getUserXLSX).Methods("GET")
	r.HandleFunc("/dpm", c.createDPM).Methods("POST")
	r.HandleFunc("/dpm", c.showDPM).Methods("GET")
	r.HandleFunc("/dpm/all", c.sendDriverDPM).Methods("GET")
	r.HandleFunc("/dpm/auto", c.autogenDPM).Methods("GET", "POST")
	r.HandleFunc("/dpm/approve", c.sendApprovalDPMS).Methods("GET")
	r.HandleFunc("/dpm/approve/{id}", c.approveDPM).Methods("PATCH")
	r.HandleFunc("/dpm/deny/{id}", c.denyDPM).Methods("PATCH")
	r.HandleFunc("/dpm/{id}", c.deleteDPM).Methods("DELETE")
	r.HandleFunc("/dpm/{id}", c.updateDPMPoints).Methods("PATCH")
	r.HandleFunc("/users", c.user).Methods("POST")
	r.HandleFunc("/users", c.users).Methods("GET")
	r.HandleFunc("/users/{id}/dpms", c.showUsersDPMS).Methods("GET")
	r.HandleFunc("/users/{id}/dpms/full", c.showFullUsersDPMS).Methods("GET")
	r.HandleFunc("/users/create", c.showUserCreate).Methods("GET")
	r.HandleFunc("/users/dequeue/{id}", c.removeUserFromQueue).Methods("POST")
	r.HandleFunc("/users/dequeue", c.removeAllFromQueue).Methods("POST")
	r.HandleFunc("/users/edit/{id}", c.renderEditUser).Methods("GET")
	r.HandleFunc("/users/edit/{id}", c.updateUser).Methods("POST", "DELETE")
	r.HandleFunc("/users/find", c.findForm).Methods("GET")
	r.HandleFunc("/users/list", c.renderUserList).Methods("GET")
	r.HandleFunc("/users/points", c.sendPointsAll).Methods("POST")
	r.HandleFunc("/users/points/{id}", c.sendPoints).Methods("POST")
	r.HandleFunc("/users/points/reset", c.resetPoints).Methods("POST")
	r.HandleFunc("/users/queue", c.renderQueue).Methods("GET")
	r.HandleFunc("/users/reset", c.reset).Methods("POST", "GET")
	r.HandleFunc("/login", c.login).Methods("POST", "GET")
	r.HandleFunc("/logout", c.logout)
	r.PathPrefix("/assets/").Handler(http.StripPrefix("/assets/", http.FileServer(http.Dir("assets/"))))
	r.PathPrefix("/views/").Handler(http.StripPrefix("/views/", http.FileServer(http.Dir("views/"))))
	http.Handle("/", r)
	fmt.Println("Server started on port", os.Getenv("PORT"))
	log.Fatal(srv.ListenAndServe())
}

// Connect to database and return a pointer to than connection
func getSession() *sqlx.DB {
	var err error
	var db *sqlx.DB
	// Change database based on if in production or not
	if !production {
		connStr := "user=tunji dbname=balloon password=" + os.Getenv("PSQL_PASS") + " sslmode=verify-full"
		db, err = sqlx.Open("postgres", connStr)
	} else {
		db, err = sqlx.Open("postgres", os.Getenv("DATABASE_URL"))
	}
	if err != nil {
		panic(err)
	}
	err = db.Ping()
	if err != nil {
		panic(err)
	}
	db.SetMaxIdleConns(4)
	return db
}
