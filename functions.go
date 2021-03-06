package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/tealeg/xlsx"
	"github.com/xlzd/gotp"
	"golang.org/x/crypto/bcrypt"
)

func (c Controller) createDPMLogic(w http.ResponseWriter, r *http.Request) {
	// Validate user
	sender, err := c.getUser(w, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// No regular users can do this
	if !sender.Admin && !sender.Sup && !sender.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if !sender.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get JSON from request body
	decoder := json.NewDecoder(r.Body)
	var d dpmRes
	// Parse JSON into DPMRes struct
	err = decoder.Decode(&d)
	if err != nil {
		out := fmt.Sprintln("Something went wrong, please try again")
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(out))
		return
	}
	// Turn simple DPM into full DPM
	dpm := generateDPM(&d)
	if dpm == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Ensure that the user has access to this function
	stmt := `SELECT admin, sup, analyst FROM users WHERE id=$1`
	var (
		admin    bool
		sup      bool
		analyst  bool
		username string
	)
	err = c.db.QueryRow(stmt, dpm.CreateID).Scan(&admin, &sup, &analyst)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Gets username, not actually needed for anything, but it serves the
	// role of checking to see if the first and last name in the database match the id being provided
	// This prevents DPMS from being created with non matching user ids and name fields
	stmt = `SELECT username FROM users WHERE id=$1 AND firstname=$2 AND lastname=$3 LIMIT 1`
	err = c.db.QueryRow(stmt, dpm.UserID, dpm.FirstName, dpm.LastName).Scan(&username)
	// If err, assume it is because a discrepancy between what's in the db and the info provided
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// If they are a regular user, they do not
	// have permission to create a dpm
	if !admin && !sup && !analyst {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Prepare query string
	dpmIn := `INSERT INTO dpms (createid, userid, firstname, lastname, block, date, starttime, endtime, dpmtype, points, notes, created, location, approved, ignored) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, false, false)`
	// Insert unapproved dpm into database
	_, err = c.db.Exec(dpmIn, dpm.CreateID, dpm.UserID, dpm.FirstName, dpm.LastName, dpm.Block, dpm.Date, dpm.StartTime, dpm.EndTime, dpm.DPMType, dpm.Points, dpm.Notes, dpm.Created, dpm.Location)
	if err != nil {
		fmt.Println("DPM failure")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	return
}

// This gets all the users, their ids, and the id of the user requesting this
// It then this data as JSON back to the client
func (c Controller) getAllUsers(w http.ResponseWriter, r *http.Request) {
	// Validate that this request is authorized
	sender, err := c.getUser(w, r)
	// If user is not signed in, redirect
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// No regular users can do this
	if !sender.Admin && !sender.Sup && !sender.Analyst {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// if user has not changed password, redirect
	if !sender.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Struct to be marshaled into JSON and sent to client
	type passUser struct {
		Names  []string `json:"names"`  // Slice of drivers' names
		Ids    []int16  `json:"ids"`    // Slice of drivers' ids
		UserID string   `json:"userID"` // Username of the user loading this resource
	}
	// Get all users
	rows, err := c.db.Query("SELECT firstname, lastname, id FROM users")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	// Slices to hold names and ids
	names := make([]string, 0)
	ids := make([]int16, 0)
	// Iterate through rows returned filling slices with info
	for rows.Next() {
		var (
			firstname string
			lastname  string
			id        int16
		)
		err = rows.Scan(&firstname, &lastname, &id)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		names = append(names, bm.Sanitize(firstname+" "+lastname))
		ids = append(ids, id)
	}
	i := int(sender.ID)
	temp := strconv.Itoa(i)
	// Fill in struct values
	pU := passUser{
		Names:  names,
		Ids:    ids,
		UserID: temp,
	}
	// Turn struct into JSON and respond with it
	j, err := json.Marshal(pU)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(j)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// Creates a user in the database
func (c Controller) createUser(w http.ResponseWriter, r *http.Request) {
	// Validate that this request is authorized
	sender, err := c.getUser(w, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if !sender.Admin {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !sender.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}

	type createUser struct {
		Username  string
		Firstname string
		Lastname  string
		Manager   string
		Role      string
		Fulltime  bool
		Queue     bool
	}

	type response struct {
		Error string
	}

	var create createUser
	// Get JSON from request body
	decoder := json.NewDecoder(r.Body)
	// Parse JSON into createUser struct
	err = decoder.Decode(&create)
	if err != nil {
		out := fmt.Sprintln("Something went wrong, please try again")
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(out))
		return
	}

	create.Username = strings.ToLower(html.UnescapeString(bm.Sanitize(strings.TrimSpace(create.Username))))
	create.Firstname = html.UnescapeString(bm.Sanitize(strings.TrimSpace(create.Firstname)))
	create.Lastname = html.UnescapeString(bm.Sanitize(strings.TrimSpace(create.Lastname)))
	create.Role = html.UnescapeString(bm.Sanitize(strings.TrimSpace(create.Role)))
	// Ensure username and firstname are not empty
	if create.Username == "" {
		j, err := json.Marshal(response{Error: "Username cannot be empty."})
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(j)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		return
	}
	if create.Firstname == "" {
		j, err := json.Marshal(response{Error: "Please provide a first name."})
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(j)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		return
	}
	// Test credentials
	var test bool
	if create.Username == "testing@testing.com" {
		test = true
	}
	u := &user{}
	err = c.db.QueryRowx("SELECT * FROM users WHERE username=$1 LIMIT 1", create.Username).StructScan(u)
	// If no error, that means user with that username already exists
	// Render template mentioning this
	// Only give error if not testing
	if err == nil && !test {
		j, err := json.Marshal(response{Error: "The username name you are trying to register is already in use, please try a different username."})
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(j)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		return
	}
	// Generate temp password
	pass := gotp.RandomSecret(16)
	// Create go routine to handle sending the email
	// Only send email if not testing
	if !test && !create.Queue {
		go sendNewUserEmail(create.Username, pass, create.Firstname, create.Lastname)
	}
	// Get password hash
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	var admin, sup, manager bool
	if create.Role == "Admin" {
		admin = true
	} else if create.Role == "Supervisor" {
		sup = true
	} else if create.Role == "Manager" {
		manager = true
	} else if create.Role != "Driver" {
		j, err := json.Marshal(response{Error: "Invalid role"})
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(j)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		return
	}
	// Determine if user is a fulltimer
	// Create user struct from form data
	u = &user{
		Username:   create.Username,
		Password:   string(hash),
		FirstName:  create.Firstname,
		LastName:   create.Lastname,
		FullTime:   create.Fulltime,
		SessionKey: gotp.RandomSecret(32), // Temp value for session, never valid
		Points:     0,
		Added:      time.Now().Format("2006-1-02 15:04:05"),
	}
	// Start transaction to rollback in case of failure
	tx, err := c.db.Begin()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	userIn := `INSERT INTO users (managerid, username, password, firstname, lastname, fulltime, admin, sup, analyst, sessionkey, added) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	_, err = tx.Exec(userIn, create.Manager, u.Username, u.Password, u.FirstName, u.LastName, u.FullTime, admin, sup, manager, u.SessionKey, u.Added)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	if create.Queue {
		var userid int
		// language=sql
		stmt := "SELECT id FROM users WHERE username=$1 AND password=$2 AND firstname=$3 AND lastname=$4"
		err = tx.QueryRow(stmt, u.Username, u.Password, u.FirstName, u.LastName).Scan(&userid)
		if err != nil {
			_ = tx.Rollback()
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Println(err)
			return
		}
		stmt = "INSERT INTO queued_accounts (userid, queuedby) VALUES ($1, $2)"
		_, err = tx.Exec(stmt, userid, sender.FirstName+" "+sender.LastName)
		if err != nil {
			_ = tx.Rollback()
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Println(err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	j, err := json.Marshal(response{Error: ""})
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, err = w.Write(j)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// Logs in the user
func (c Controller) logInUser(w http.ResponseWriter, r *http.Request) {
	// Struct for later use
	u := &user{}
	// Get user input
	user := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	pass := r.FormValue("password")
	// Find user in database
	err := c.db.QueryRowx("SELECT * FROM users WHERE username=$1 LIMIT 1", user).StructScan(u)
	// If they do not exist, complain
	if err != nil {
		fmt.Println("Here")
		fmt.Println(err)
		out := "Username or password was incorrect, please try again."
		c.loginError(w, r, out, html.UnescapeString(bm.Sanitize(user)))
		return
	}
	// Validate password
	err = bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(pass))
	// If passwords do not match, render template with message
	if err != nil {
		fmt.Println(err)
		err = bcrypt.CompareHashAndPassword([]byte(u.Password), []byte((strings.TrimSpace(pass))))
		if err != nil {
			out := "Username or password was incorrect, please try again."
			c.loginError(w, r, out, html.UnescapeString(bm.Sanitize(user)))
			return
		}
	}
	// Create a session for the user
	sk, err := c.cookieSignIn(w, r)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Something went wrong, pleae try again."))
		return
	}
	// Set user's session key
	u.SessionKey = sk
	// Update user in database to contain this new session
	update := `UPDATE users SET sessionkey=$1 WHERE id=$2`
	_, err = c.db.Exec(update, u.SessionKey, u.ID)
	if err != nil {
		fmt.Println(err)
		out := fmt.Sprintln("Something went wrong")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(out))
		return
	}
	// If signing in with temporary password, make user change it
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Redirect user after successful login
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logic for changing the password of a user
func (c Controller) changeUserPassword(w http.ResponseWriter, r *http.Request) {
	// Validate user
	u, err := c.getUser(w, r)
	if err != nil {
		c.renderLogin(w, r)
		return
	}
	// Get old password and make sure it matches what is in the database
	og := r.FormValue("originalPass")
	// Ensure new password matches what's in db
	err = bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(og))
	// If passwords do not match, inform user
	if err != nil {
		err = bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(strings.TrimSpace(og)))
		if err != nil {
			out := "Please ensure that you are inputting your old password correctly."
			c.changePasswordError(w, r, out)
			return
		}
	}
	// Get both copies of new password and ensure they are the same
	pass1 := r.FormValue("pass1")
	pass2 := r.FormValue("pass2")
	if pass1 != pass2 {
		out := "Please ensure that your new passwords match."
		c.changePasswordError(w, r, out)
		return
	}
	// Check is password is shorter than 8 characters
	if len(pass1) < 8 {
		out := "Please make your password at least eight characters long."
		c.changePasswordError(w, r, out)
		return
	}
	// Check if password has trailing or leading whitespace
	if len(strings.TrimSpace(pass1)) < len(pass1) {
		out := "Please make sure that your password does not contain any trailing or leading whitespace."
		c.changePasswordError(w, r, out)
		return
	}
	// If user enters their temporary password for their new password, complain
	if pass1 == og {
		out := "Please make your new password different from your temporary one."
		c.changePasswordError(w, r, out)
		return
	}
	// Create a new session for the user
	sk, err := c.cookieSignIn(w, r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Something went wrong with creating your session."))
		return
	}
	u.SessionKey = sk
	// Hash their new password and store it in struct
	hash, err := bcrypt.GenerateFromPassword([]byte(pass1), bcrypt.DefaultCost)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	u.Password = string(hash)
	// They have changed password, so they are definitely not using temp pass any more
	u.Changed = true
	// Update user in database to contain this new session and new password
	update := `UPDATE users SET sessionkey=$1, changed=$2, password=$3 WHERE id=$4`
	_, err = c.db.Exec(update, u.SessionKey, u.Changed, u.Password, u.ID)
	if err != nil {
		out := fmt.Sprintln("Something went wrong")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(out))
		return
	}
	// Send password change email
	go sendPasswordChanged(u.Username, u.FirstName, u.LastName)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logs a user out
func (c Controller) logoutUser(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// If can't find user, same behavior as regular logout
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Set session key to random string
	u.SessionKey = gotp.RandomSecret(32)
	// Fetch session
	sess, err := c.store.Get(r, "dpm_cookie")
	// Even if session somehow does not exit at this stage, store.Get
	// generates a new session so if this step fails, something weird is going on
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Expire session
	sess.Options.MaxAge = -1
	err = c.store.Save(r, w, sess)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Update user in db with invalid session
	update := `UPDATE users SET sessionkey=$1 WHERE id=$2`
	_, err = c.db.Exec(update, u.SessionKey, u.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// resetPassword handles resetting a user's password on admin request
func (c Controller) resetPassword(w http.ResponseWriter, r *http.Request) {
	// If user not logged in, redirect
	sender, err := c.getUser(w, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// If user is not an admin, 404
	if !sender.Admin {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Sanitize username
	username := html.UnescapeString(bm.Sanitize(strings.TrimSpace(r.FormValue("username"))))
	if c.resetPassHelper(w, r, username, sender) {
		// Display success message
		out := "User password successfully reset"
		c.resetPasswordMessage(w, r, out)
	}
}

// callAutoSubmit autogenerates a list of DPMS from whentowork and submits them
func (c Controller) callAutoSubmit(w http.ResponseWriter, r *http.Request) {
	// Get user and validate
	sender, err := c.getUser(w, r)
	if err != nil {
		fmt.Println(err)
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// No regular users can do this
	if !sender.Admin && !sender.Sup && !sender.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// If still using temp password, redirect
	if !sender.Changed {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Regenerate DPMS
	// This is inefficient because I already generate these when the user submits a get to /dpm/all
	// The reason why I generate again is to help protect against the unlikely odds of someone changing data on their end and sending it back to me to submit
	dpms, err := autoGen(c.db)
	// If error, render the autogenErr template stating this
	if err != nil {
		n := navbar{
			Admin:   sender.Admin,
			Sup:     sender.Sup,
			Analyst: sender.Analyst,
		}
		auto := err.Error()
		err = c.tpl.ExecuteTemplate(w, "autogenErr.gohtml", map[string]interface{}{
			"Nav": n, "Err": auto,
		})
		if err != nil {
			out := fmt.Sprintln("Something went wrong, please try again")
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(out))
			return
		}
		return
	}
	err = autoSubmit(c.db, dpms, sender.ID)
	// If error, render the autogenErr template stating this
	if err != nil {
		n := navbar{
			Admin:   sender.Admin,
			Sup:     sender.Sup,
			Analyst: sender.Analyst,
		}
		auto := err.Error()
		err = c.tpl.ExecuteTemplate(w, "autogenErr.gohtml", map[string]interface{}{
			"Nav": n, "Err": auto,
		})
		if err != nil {
			out := fmt.Sprintln("Something went wrong, please try again")
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(out))
			return
		}
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// sendApprovalLogic sends all unapproved DPMS to the user requesting the page
// Helper function for SendApprovalDPM
func (c Controller) sendApprovalLogic(w http.ResponseWriter, r *http.Request) {
	// If can't find user/user not logged in, redirect to login page
	u, err := c.getUser(w, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins and analysts can do this
	if !u.Admin && !u.Analyst {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Redirect is still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Variables need for loop
	var stmt, firstname, lastname, block, location, date, startTime, endTime, dpmType, points, notes, created, supFirst, supLast, id string
	// Variable containing the id of the supervisor who submitted each dpm
	var supID int16
	var rows *sql.Rows
	if u.Admin {
		// Query that gets most of the relevant information about each non-approved dpm
		// language=sql
		stmt = `SELECT id, createid, firstname, lastname, block, location, date, starttime, endtime, dpmtype, points, notes, created FROM dpms WHERE approved=false AND ignored=false ORDER BY created DESC`
		// If analyst, there is a more complicated query to get the dpms
	} else if u.Analyst {
		// language=sql
		stmt = `SELECT a.id, a.createid, a.firstname, a.lastname, a.block, a.location, a.date, a.starttime, a.endtime, a.dpmtype, a.points, a.notes, a.created FROM dpms a
		JOIN users b ON b.id=a.userid
		WHERE approved=false AND ignored=false AND managerid=$1 ORDER BY created DESC`
	}
	// Query that gets the name of the supervisor that submitted each dpm
	supQuery := `SELECT firstname, lastname FROM users WHERE id=$1`
	ds := make([]dpmApprove, 0)
	if u.Admin {
		rows, err = c.db.Query(stmt)
		// If they are an analyst, I need to pass their id into the query
	} else if u.Analyst {
		rows, err = c.db.Query(stmt, u.ID)
	}
	defer rows.Close()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var dd dpmApprove
	for rows.Next() {
		// Get variables from the row
		err = rows.Scan(&id, &supID, &firstname, &lastname, &block, &location, &date, &startTime, &endTime, &dpmType, &points, &notes, &created)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Ensure that positive points start with a '+'
		if string(points[0]) != "-" {
			points = "+" + points
		}
		// Find sup who submitted this DPM
		err = c.db.QueryRow(supQuery, supID).Scan(&supFirst, &supLast)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Create DPMApprove struct to pass into slice
		dd = dpmApprove{
			ID:        bm.Sanitize(id),
			Name:      bm.Sanitize(firstname + " " + lastname),
			SupName:   bm.Sanitize(supFirst + " " + supLast),
			Block:     bm.Sanitize(block),
			Location:  bm.Sanitize(location),
			Date:      bm.Sanitize(date),
			StartTime: bm.Sanitize(startTime),
			EndTime:   bm.Sanitize(endTime),
			DPMType:   bm.Sanitize(dpmType),
			Points:    bm.Sanitize(points),
			Notes:     bm.Sanitize(notes),
			Created:   bm.Sanitize(created),
		}
		ds = append(ds, dd)
	}
	// Turn slice into JSON and respond with it
	j, err := json.Marshal(ds)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(j)
}

// sendDriverLogic handles sending simplified DPMs to drivers
func (c Controller) sendDriverLogic(w http.ResponseWriter, r *http.Request) {
	// If can't find user/user not logged in, redirect to login page
	u, err := c.getUser(w, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	stmt := `SELECT firstname, lastname, block, location, date, starttime, endtime, dpmtype, points, notes FROM dpms WHERE userid=$1 AND approved=true AND ignored=false AND created > now() - interval '6 months' ORDER BY created DESC`
	ds := make([]dpmDriver, 0)
	rows, err := c.db.Queryx(stmt, u.ID)
	defer rows.Close()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var dd dpmDriver
	for rows.Next() {
		err = rows.StructScan(&dd)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		dd.Notes = html.UnescapeString(dd.Notes)
		dd.FirstName = html.UnescapeString(dd.FirstName)
		dd.LastName = html.UnescapeString(dd.LastName)
		dd.Block = html.UnescapeString(dd.Block)
		dd.Location = html.UnescapeString(dd.Location)
		if string(dd.Points[0]) != "-" {
			dd.Points = "+" + dd.Points
		}
		ds = append(ds, dd)
	}
	// Turn slice into JSON and respond with it
	j, err := json.Marshal(ds)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(j)
}

// approveDPMLogic handles logic for approving a DPM
func (c Controller) approveDPMLogic(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins and analyst can do this
	if !u.Admin && !u.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Temporary struct to hold response from client
	type approveDPM struct {
		Points string
		Name   string
	}
	a := approveDPM{}
	// Get JSON from request body
	decoder := json.NewDecoder(r.Body)
	// Parse JSON to get points value and name
	err = decoder.Decode(&a)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Parse the URL and get the id from the URL
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var secondID, managerid int
	var fulltime bool
	var dpmtype, username, firstname, lastname, manager, date string
	// This checks that this dpm id relates to a real dpm and gets the fulltime status, username, and dpm type of the driver
	stmt := `SELECT a.id, a.fulltime, a.username, a.firstname, a.lastname, b.dpmtype, b.date FROM users a
	JOIN dpms b ON a.id=b.userid
	WHERE b.id=$1;`
	err = c.db.QueryRow(stmt, id).Scan(&secondID, &fulltime, &username, &firstname, &lastname, &dpmtype, &date)
	// If this fails, assume the ID is not valid and abort
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Println(err)
		return
	}

	// Select the manager ID for the driver who owns this DPM
	stmt = `SELECT id, firstname || ' ' || lastname AS manager
				FROM users
				WHERE id = (SELECT managerid
    				FROM users a
    				JOIN dpms b ON a.id=b.userid
    				WHERE b.id=$1);`
	err = c.db.QueryRow(stmt, id).Scan(&managerid, &manager)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}

	// If not an admin, make sure they have access to this dpm
	if !u.Admin {
		// If ids do not match, abort
		if managerid != int(u.ID) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	// Update specified DPM to make approved equal to true
	update := `UPDATE dpms SET approved=true WHERE id=$1`
	_, err = c.db.Exec(update, id)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	// Split name based on spaces
	ns := strings.Split(a.Name, " ")
	// Get first name
	first := bm.Sanitize(ns[0])
	last := ""
	// Join indexes after 0 into last name string and sanitize
	// If last name exists, set it to the remainder of slice joined together
	if len(ns) > 1 {
		ns = append(ns[:0], ns[1:]...)
		last = bm.Sanitize(strings.Join(ns, " "))
	}
	// Convert points into int
	points, err := strconv.Atoi(a.Points)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	// Update user's point balance to reflect the new points
	update = `UPDATE users set points=points + $1 WHERE firstname=$2 AND lastname=$3`
	_, err = c.db.Exec(update, points, first, last)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}

	sendDate, err := time.Parse(time.RFC3339, date)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if username != "testing@testing.com" && !c.isUserQueued(username, firstname, lastname) {
		// Send point email
		go sendDPMEmail(username, firstname, lastname, dpmtype, sendDate.Format("01/02/06"), manager, points)
	}
	w.WriteHeader(http.StatusOK)
}

// denyDPMLogic handles logic for denying a dpm
func (c Controller) denyDPMLogic(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins and analysts can do this
	if !u.Admin && !u.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Parse URL for id of DPM
	vars := mux.Vars(r)
	// Convert id to int
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var secondID, managerid int
	// All this does is check that this dpm id relates to a real dpm
	stmt := `SELECT id FROM dpms WHERE id=$1`
	err = c.db.QueryRow(stmt, id).Scan(&secondID)
	// If this fails, assume the ID is not valid and abort
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Println(err)
		return
	}
	// If not an admin, make sure they have access to this dpm
	if !u.Admin {
		// Select the manager ID for the driver who owns this DPM
		stmt := `SELECT managerid FROM users a
		JOIN dpms b ON a.id=b.userid
		WHERE b.id=$1;`
		err = c.db.QueryRow(stmt, id).Scan(&managerid)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Println(err)
			return
		}
		// If ids do not match, abort
		if managerid != int(u.ID) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	// Update specified DPM to set ignored to true and set approved to true. Set approved to true to indicate that the dpm has been looked at
	update := `UPDATE dpms SET approved=false, ignored=true WHERE id=$1`
	_, err = c.db.Exec(update, id)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// usersXLSX gets data from the users table and creates an excel file
func (c Controller) usersXLSX(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins and analysts can do this
	if !u.Admin && !u.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// variables to extract row data into
	var (
		firstname string
		lastname  string
		points    string
		manager   string
	)
	// Query database for required info
	stmt := `SELECT u.lastname, u.firstname, u.points, b.firstname || ' ' || b.lastname AS manager
			FROM users AS u
			INNER JOIN users AS b ON u.managerid=b.id
			WHERE u.username != 'testing@testing.com'
			ORDER BY lastname, firstname`
	rows, err := c.db.Query(stmt)
	defer rows.Close()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Prepare for writing to excel sheet
	var file *xlsx.File
	var sheet *xlsx.Sheet
	var row *xlsx.Row
	var cell *xlsx.Cell
	var style *xlsx.Style

	style = xlsx.NewStyle()
	style.Font.Bold = true
	file = xlsx.NewFile()
	sheet, err = file.AddSheet("Users")
	if err != nil {
		fmt.Printf(err.Error())
		return
	}
	headers := []string{
		"Last Name",
		"First Name",
		"Points",
		"Manager",
	}

	// Add headers
	row = sheet.AddRow()
	for _, header := range headers {
		cell = row.AddCell()
		cell.SetStyle(style)
		cell.Value = header
	}

	// new style for text wrapping
	style = xlsx.NewStyle()
	style.Alignment.WrapText = true
	for rows.Next() {
		row = sheet.AddRow()
		// Extract data into variables
		err = rows.Scan(&lastname, &firstname, &points, &manager)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if string(points[0]) != "-" {
			points = "+" + points
		}
		rowValues := []string{
			html.UnescapeString(lastname),
			html.UnescapeString(firstname),
			points,
			html.UnescapeString(manager),
		}
		for _, value := range rowValues {
			cell = row.AddCell()
			cell.SetStyle(style)
			cell.Value = value
		}
	}
	// Create a mutex lock so file writing does not cause problems
	var mu sync.Mutex
	// Lock this process so only one write happens at a time
	mu.Lock()
	defer mu.Unlock()
	err = sheet.SetColWidth(0, 1, 20)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(3, 3, 20)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = file.Save("Users.xlsx")
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
	}
	returnFile(w, "Users.xlsx")
}

// dpmXLSX creates an excel file data from the dpms table
func (c Controller) dpmXLSX(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins and analysts can do this
	if !u.Admin && !u.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	params := r.URL.Query()
	// Get all DPMS?
	var getAll bool
	reset, ok := params["reset"]
	if ok && reset[0] == "on" {
		getAll = true
	}
	endDate, ok := params["end"]
	// Not valid request
	if !ok && !getAll {
		w.WriteHeader(http.StatusConflict)
		return
	}
	startDate, ok := params["start"]
	if !ok && !getAll {
		w.WriteHeader(http.StatusConflict)
		return
	}
	// Convert dates from url string into golang time type
	start, err := time.Parse("2006-1-2", startDate[0])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusConflict)
		return
	}
	end, err := time.Parse("2006-1-2", endDate[0])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusConflict)
		return
	}
	// if end time is before start time and they are not trying to get all dpms, that is an invalid time range
	if end.Before(start) && !getAll {
		w.WriteHeader(http.StatusConflict)
		return
	}
	// Create a bunch of variables to extract row data into and predeclare things for query
	var (
		rows      *sql.Rows
		stmt      string
		firstname string
		lastname  string
		block     string
		date      string
		dpmtype   string
		points    string
		notes     string
		created   string
		location  string
		startTime string
		endtime   string
		createdBy string
		approved  bool
		ignored   bool
	)

	if u.Analyst {
		if getAll {
			stmt = `SELECT d.firstname, d.lastname, d.block, d.date, d.dpmtype, d.points, d.notes, d.created, d.location, d.starttime, d.endtime, d.approved, d.ignored,
       		(SELECT firstname || ' ' || lastname AS supname FROM users WHERE d.createid=id)
			FROM dpms d INNER JOIN users u ON d.userid=u.id
			WHERE u.managerid = $1
			ORDER BY date DESC, created DESC`
			rows, err = c.db.Query(stmt, u.ID)
		} else {
			stmt = `SELECT d.firstname, d.lastname, d.block, d.date, d.dpmtype, d.points, d.notes, d.created, d.location, d.starttime, d.endtime, d.approved, d.ignored,
       		(SELECT firstname || ' ' || lastname AS supname FROM users WHERE d.createid=id)
			FROM dpms d INNER JOIN users u ON d.userid=u.id
			WHERE u.managerid = $1 AND created <= $2 AND created >= $3
			ORDER BY date DESC, created DESC`
			rows, err = c.db.Query(stmt, u.ID, endDate[0], startDate[0])
		}
	} else {
		if getAll {
			stmt = `SELECT d.firstname, d.lastname, d.block, d.date, d.dpmtype, d.points, d.notes, d.created, d.location, d.starttime, d.endtime, d.approved, d.ignored, u.firstname || ' ' || u.lastname AS supname FROM dpms d
			INNER JOIN users u ON d.createid=u.id
			ORDER BY date DESC, created DESC`
			rows, err = c.db.Query(stmt)
		} else {
			stmt = `SELECT d.firstname, d.lastname, d.block, d.date, d.dpmtype, d.points, d.notes, d.created, d.location, d.starttime, d.endtime, d.approved, d.ignored, u.firstname || ' ' || u.lastname AS supname FROM dpms d
			INNER JOIN users u ON d.createid=u.id
			WHERE created <= $1 AND created >= $2 ORDER BY date DESC, created DESC`
			rows, err = c.db.Query(stmt, endDate[0], startDate[0])
		}
	}
	defer rows.Close()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Prepare for writing to excel sheet
	var file *xlsx.File
	var sheet *xlsx.Sheet
	var row *xlsx.Row
	var cell *xlsx.Cell
	var style *xlsx.Style

	style = xlsx.NewStyle()
	style.Font.Bold = true
	file = xlsx.NewFile()
	sheet, err = file.AddSheet("DPMs")
	if err != nil {
		fmt.Printf(err.Error())
		return
	}
	headers := []string{
		"First Name",
		"Last Name",
		"Block",
		"Location",
		"Start Time",
		"End Time",
		"Date",
		"Type",
		"Points",
		"Notes",
		"Status",
		"Created",
		"Created By",
	}

	// Add headers
	row = sheet.AddRow()
	for _, header := range headers {
		cell = row.AddCell()
		cell.SetStyle(style)
		cell.Value = header
	}

	// new style for text wrapping
	style = xlsx.NewStyle()
	style.Alignment.WrapText = true
	for rows.Next() {
		// Create row for values
		row = sheet.AddRow()
		// Scan row data into variables
		err = rows.Scan(&firstname, &lastname, &block, &date, &dpmtype, &points, &notes, &created, &location, &startTime, &endtime, &approved, &ignored, &createdBy)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Format date, created, startTime, and endTime into a more user friendly data format
		date = formatDate(date)
		created = formatCreatedDate(created)
		startTime = startTime[11:13] + startTime[14:16]
		endtime = endtime[11:13] + endtime[14:16]
		if string(points[0]) != "-" {
			points = "+" + points
		}
		status := c.getStatus(approved, ignored)
		rowValues := []string{
			html.UnescapeString(firstname),
			html.UnescapeString(lastname),
			html.UnescapeString(block),
			html.UnescapeString(location),
			startTime,
			endtime,
			date,
			dpmtype,
			points,
			html.UnescapeString(notes),
			status,
			created,
			createdBy,
		}
		for _, value := range rowValues {
			cell = row.AddCell()
			cell.SetStyle(style)
			cell.Value = value
		}
	}
	// Create a mutex lock so file writing does not cause problems
	var mu sync.Mutex
	// Lock this process so only one write happens at a time
	mu.Lock()
	defer mu.Unlock()
	err = sheet.SetColWidth(0, 2, 20)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(2, 6, 15)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(7, 7, 40)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(9, 9, 60)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(10, 10, 30)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(11, 11, 20)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = sheet.SetColWidth(12, 12, 20)
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = file.Save("DPMs.xlsx")
	if err != nil {
		fmt.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
	}
	returnFile(w, "DPMs.xlsx")
}

// fillCreateUser fills out the create user form
func (c Controller) fillCreateUser(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get search from url
	name := bm.Sanitize(html.UnescapeString(strings.TrimSpace(r.URL.Query().Get("name"))))
	// Split name into first and last, if applicable
	ns := strings.Split(name, " ")
	var first, last string
	first = ns[0]
	// Join indexes after 0 into last name string and sanitize
	// If last name exists, form a different query
	if len(ns) > 1 {
		ns = append(ns[:0], ns[1:]...)
		last = html.UnescapeString(bm.Sanitize(strings.Join(ns, " ")))
		c.createUserFill(w, r, first, last)
		return
	} else { // Handle first name only
		c.createUserFill(w, r, first, last)
		return
	}
}

// editUser updates the user based on the data returned from the form
func (c Controller) editUser(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get user id from url and convert it to an int
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Get reset status
	reset := false
	if r.FormValue("reset") == "on" {
		reset = true
	}

	// Get form information
	username := html.UnescapeString(bm.Sanitize(strings.TrimSpace(r.FormValue("username"))))
	firstname := html.UnescapeString(bm.Sanitize(strings.TrimSpace(r.FormValue("firstname"))))
	lastname := html.UnescapeString(bm.Sanitize(strings.TrimSpace(r.FormValue("lastname"))))
	points := bm.Sanitize(strings.TrimSpace(r.FormValue("points")))
	// Get fulltime status
	fulltime := false
	if r.FormValue("fulltime") == "on" {
		// See if person is already fulltime
		stmt := `SELECT fulltime FROM users WHERE id=$1`
		err = c.db.QueryRow(stmt, id).Scan(&fulltime)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// If user is becoming a fulltimer, set their point balance to 0, and ignore unapproved dpms
		if !fulltime {
			fulltime = true
			points = "0"
			stmt = `UPDATE dpms SET ignored = TRUE WHERE userid=$1 AND approved=TRUE`
			_, err = c.db.Exec(stmt, id)
			if err != nil {
				fmt.Println(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
	}
	// Convert manager id to an int
	manager := r.FormValue("manager")
	managerid, err := strconv.Atoi(manager)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var (
		isManager bool
		isAdmin   bool
	)
	stmt := `SELECT admin, analyst FROM users WHERE id=$1`
	err = c.db.QueryRow(stmt, managerid).Scan(&isAdmin, &isManager)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Return 409 if (probably malicious) user tries to assign another user to be managed by someone
	// that is not a manger or admin
	if !(isAdmin || isManager) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	role := bm.Sanitize(r.FormValue("role"))
	// Make sure that role is lowered, for consistency
	role = strings.ToLower(role)
	// Assign values to roles
	var admin, analyst, sup bool
	if role == "admin" {
		admin = true
	} else if role == "manager" {
		analyst = true
	} else if role == "supervisor" {
		sup = true
	}
	stmt = `UPDATE users SET admin=$1, analyst=$2, sup=$3, username=$4, firstname=$5, lastname=$6, managerid=$7, fulltime=$8, points=$9 WHERE id=$10`
	_, err = c.db.Exec(stmt, admin, analyst, sup, username, firstname, lastname, managerid, fulltime, points, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// If reset is true, reset password
	if reset {
		// Let the function handle the redirect/error displaying
		if !c.resetPassHelper(w, r, username, u) {
			return
		}
	}
	http.Redirect(w, r, r.URL.String(), http.StatusFound)
	return
}

// deleteUser deletes a user from the database
func (c Controller) deleteUser(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get user id from url and convert it to an int
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Make sure admins can't delete themselves
	if u.ID == int16(id) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// Delete all of the users dpms
	stmt := `DELETE FROM dpms WHERE userid=$1`
	_, err = c.db.Exec(stmt, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Change dpms potentially created by deleted user to show that they are created by the admin deleting the user
	stmt = `UPDATE dpms SET createid=$1 WHERE createid=$2`
	_, err = c.db.Exec(stmt, u.ID, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Make any users potentially managed by deleted user managed by admin calling the route
	stmt = `UPDATE users SET managerid=$1 WHERE managerid=$2`
	_, err = c.db.Exec(stmt, u.ID, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Delete potential entry from the queued_accounts table
	stmt = `DELETE FROM queued_accounts WHERE userid=$1`
	_, err = c.db.Exec(stmt, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Delete user from database
	stmt = `DELETE FROM users WHERE id=$1`
	_, err = c.db.Exec(stmt, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	return
}

// sendPointsToAll sends each user their point balance via email
func (c Controller) sendPointsToAll(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get relevant info and ignore testing users
	rows, err := c.db.Query(`SELECT username, points, firstname, lastname FROM users WHERE username != 'testing@testing.com'`)
	defer rows.Close()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var (
		username  string
		points    string
		firstname string
		lastname  string
	)
	// Scan values into variables and call email functions
	for rows.Next() {
		err = rows.Scan(&username, &points, &firstname, &lastname)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !c.isUserQueued(username, firstname, lastname) {
			go sendPointsBalance(username, firstname, lastname, points)
		}
	}
	w.WriteHeader(http.StatusOK)
	return
}

// sendUserPoints sends the point balance of a specific user
func (c Controller) sendUserPoints(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get user id from url and convert it to an int
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var (
		username  string
		points    string
		firstname string
		lastname  string
	)
	// Get user name of account based on id
	stmt := `SELECT username FROM users WHERE id=$1`
	err = c.db.QueryRow(stmt, id).Scan(&username)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// If this user is a testing account, return
	if username == "testing@testing.com" {
		w.WriteHeader(http.StatusFound)
		return
	}
	stmt = `SELECT points, firstname, lastname FROM users WHERE username != 'testing@testing.com' AND id=$1`
	err = c.db.QueryRow(stmt, id).Scan(&points, &firstname, &lastname)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !c.isUserQueued(username, firstname, lastname) {
		go sendPointsBalance(username, firstname, lastname, points)
	}
	w.WriteHeader(http.StatusOK)
	return
}

// resetPartTimePoints resets the point values for part timers
func (c Controller) resetPartTimePoints(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Set point balance for all part timers to be 0
	stmt := `UPDATE users SET points=0 WHERE fulltime=false`
	_, err = c.db.Exec(stmt)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Ignore all the dpms belonging to partimers that have been approved (already looked at)
	stmt = `UPDATE dpms a SET ignored = TRUE WHERE a.userid IN (
		SELECT id FROM users WHERE fulltime=FALSE
		) AND a.approved = TRUE;`

	_, err = c.db.Exec(stmt)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	return
}

// updateDPMPoints updates the points value for a specific DPM
func (c Controller) updateDPMPoints(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins and analyst can do this
	if !u.Admin && !u.Analyst {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Temporary struct to hold response from client
	type pointStruct struct {
		Points string
	}
	a := pointStruct{}
	// Get JSON from request body
	decoder := json.NewDecoder(r.Body)
	// Parse JSON to get points value and name
	err = decoder.Decode(&a)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Parse the URL and get the id from the URL
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var managerid int
	// If not an admin, make sure they have access to this dpm
	if !u.Admin {
		// Select the manager ID for the driver who owns this DPM
		stmt := `SELECT managerid FROM users a
		JOIN dpms b ON a.id=b.userid
		WHERE b.id=$1;`
		err = c.db.QueryRow(stmt, id).Scan(&managerid)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Println(err)
			return
		}
		// If ids do not match, abort
		if managerid != int(u.ID) {
			fmt.Println("Not authorized")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	points, err := strconv.Atoi(a.Points)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var dpmtype, pointString string
	err = c.db.QueryRow(`SELECT dpmtype FROM dpms WHERE id=$1`, id).Scan(&dpmtype)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// Make sure DPM type is correct
	switch {
	case points == 0, points < -1:
		pointString = fmt.Sprintf("(%d Points)", points)
	case points == -1:
		pointString = fmt.Sprintf("(%d Point)", points)
	case points == 1:
		pointString = fmt.Sprintf("(+%d Point)", points)
	default:
		pointString = fmt.Sprintf("(+%d Points)", points)
	}
	// Add space to dpmtype to make string manipulation easier
	dpmtype += " "
	// Gets letter of DPM, eg. G
	letter := fmt.Sprintf("%s", dpmtype[5:6])
	// Gets the part of dpm past Type[G]:, but minus the points in parenthesis
	description := strings.Trim(strings.Replace(dpmtype[8:len(dpmtype)-12], "(", "", -1), " ")
	out := fmt.Sprintf("Type %s: %s %s", letter, description, pointString)
	// Update specified DPM to reflect new points value
	update := `UPDATE dpms SET points=$1, dpmtype=$2 WHERE id=$3`
	_, err = c.db.Exec(update, points, out, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	return
}

// sendUsersDPMSs gets complete DPM data for a user and sends it to an admin
func (c Controller) sendUsersDPMs(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Get user id from the url
	vars := mux.Vars(r)
	id := vars["id"]
	dpms := make([]dpmAdmin, 0)
	rows, err := c.db.Queryx(`SELECT d.id, d.firstname, d.lastname, d.block, d.location, d.date, d.starttime, d.endtime, d.dpmtype, d.points, d.notes, d.approved, d.ignored, d.created, u.firstname || ' ' || u.lastname AS supname
									FROM dpms AS d INNER JOIN users AS u ON d.createid=u.id
									WHERE userid=$1 ORDER BY date DESC, created DESC;`, id)
	defer rows.Close()
	var dd dpmAdmin
	for rows.Next() {
		err = rows.StructScan(&dd)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		dd.Notes = html.UnescapeString(dd.Notes)
		dd.FirstName = html.UnescapeString(dd.FirstName)
		dd.LastName = html.UnescapeString(dd.LastName)
		dd.Block = html.UnescapeString(dd.Block)
		dd.Location = html.UnescapeString(dd.Location)
		if string(dd.Points[0]) != "-" {
			dd.Points = "+" + dd.Points
		}
		dpms = append(dpms, dd)
	}
	// Turn slice into JSON and respond with it
	j, err := json.Marshal(dpms)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(j)
}

// removeDPMPostLogic ignores a DPM after it has already been created.
// This also adjusts the points balance to reflect the removal of the DPM
func (c Controller) removeDPMPostLogic(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Parse URL for id of DPM
	vars := mux.Vars(r)
	// Convert id to int
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var (
		points   int
		approved bool
		ignored  bool
	)

	// Get the points value for the DPM
	stmt := `SELECT points, approved, ignored FROM dpms WHERE id=$1`
	err = c.db.QueryRow(stmt, id).Scan(&points, &approved, &ignored)
	// If this fails, assume the ID is not valid and abort
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !(approved == false && ignored == true) {
		points *= -1
		_, err = c.db.Exec(`UPDATE users SET points = points + $1 
							WHERE id = (SELECT userid FROM dpms WHERE id = $2);`, points, id)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	_, err = c.db.Exec(`UPDATE dpms SET approved=false, ignored=true WHERE id=$1`, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
}

// dequeueUser removes a specific user from the queue
func (c Controller) dequeueUser(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}
	// Parse URL for id of DPM
	vars := mux.Vars(r)
	// Convert id to int
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var username, firstname, lastname string

	// language=sql
	stmt := `SELECT username, firstname, lastname FROM users u
	INNER JOIN queued_accounts qa on u.id = qa.userid
	WHERE u.id=$1;`
	err = c.db.QueryRow(stmt, id).Scan(&username, &firstname, &lastname)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	pass := gotp.RandomSecret(16)
	// Get password hash
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	stmt = `UPDATE users SET password=$1 WHERE id=$2`
	_, err = c.db.Exec(stmt, string(hash), id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	stmt = `DELETE FROM queued_accounts WHERE userid=$1`
	_, err = c.db.Exec(stmt, id)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if username != "testing@testing.com" {
		go sendNewUserEmail(username, pass, firstname, lastname)
	} else {
		fmt.Println("Would have sent email, but user is a test account")
	}
	w.WriteHeader(http.StatusOK)
}

// dequeue removes all users from the queue
func (c Controller) dequeue(w http.ResponseWriter, r *http.Request) {
	u, err := c.getUser(w, r)
	// Validate user
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		fmt.Println(err)
		return
	}
	// Only admins can do this
	if !u.Admin {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Redirect if still on temporary password
	if !u.Changed {
		http.Redirect(w, r, "/change", http.StatusFound)
		return
	}

	type dequeue struct {
		ID        int
		Username  string
		Firstname string
		Lastname  string
	}
	dSlice := make([]dequeue, 0)

	// language=sql
	stmt := `SELECT u.id, u.username, u.firstname, u.lastname FROM users u
	INNER JOIN queued_accounts qa on u.id = qa.userid`
	err = c.db.Select(&dSlice, stmt)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for _, user := range dSlice {
		// language=sql
		pass := gotp.RandomSecret(16)
		// Get password hash
		hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		stmt = `UPDATE users SET password=$1 WHERE id=$2`
		_, err = c.db.Exec(stmt, string(hash), user.ID)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		stmt = `DELETE FROM queued_accounts WHERE userid=$1`
		_, err = c.db.Exec(stmt, user.ID)
		if err != nil {
			fmt.Println(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if user.Username != "testing@testing.com" {
			go sendNewUserEmail(user.Username, pass, user.Firstname, user.Lastname)
		} else {
			fmt.Println("Would have sent email, but user is a test account")
		}
	}
	w.WriteHeader(http.StatusOK)
}
