package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Map to match when2works mapping
var days = map[string]string{
	"Monday":    "0",
	"Tuesday":   "1",
	"Wednesday": "2",
	"Thursday":  "3",
	"Friday":    "4",
	"Saturday":  "5",
	"Sunday":    "6",
}

// Create a slice of DPMRes and return them
// Function handling the rendering of the autogen page will call this function, get the slice of dpms
// and pass it into the template
// The template will loop over the slice and print out the autogenerated stuff
// There will be a submit button to submit all the autogenerated dpms
// Maybe have a server wide variable, or a file that the server reads from, that keeps track of the last time some one auto generated DPMs

// AutoGen parses the when2work page for the current date, of the server (EST), and generates a slice of DPMDriver's
// which are a simplified version of a full DPM
func autoGen() ([]dpmDriver, error) {
	// Check to make sure that no dpms have be autosubmitted for today
	err := checkLastSubmission()
	if err != nil {
		return nil, err
	}
	dpms := make([]dpmDriver, 0)
	// Post Request to signin
	// Can get SID from this line
	// <a href="#" onclick="window.open('/cgi-bin/w2wG3.dll/mgrcontact.htm?SID=42041057341E7',
	response, err := http.PostForm(
		"https://whentowork.com/cgi-bin/w2w.dll/login",
		url.Values{
			"Launch":          {""},
			"LaunchParams":    {""},
			"Password1":       {os.Getenv("W2W_PASS")},
			"Submit1":         {"Please Wait..."},
			"UserId1":         {os.Getenv("W2W_USER")},
			"captca_required": {"false"},
			"name":            {"signin"},
		},
	)
	if err != nil {
		fmt.Println(err)
		return nil, errors.New("failed to create post request")
	}
	defer response.Body.Close()

	// Read post response
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, errors.New("failed to read response from post to when2work")
	}

	// Find SID from post request
	re := regexp.MustCompile(`SID=\w+`)
	sid := string(re.Find([]byte(re.Find(body))))
	if sid == "" {
		return nil, errors.New("failed to get SID from when2work response")
	}

	// Get day of week, so that I am parsing the correct date
	weekday := days[string(time.Now().Weekday())]
	// Reference URL
	// https://www7.whentowork.com/cgi-bin/w2wG4.dll/mgrschedule?SID=42041057341E7&lmi=1&view=Pos
	response, err = http.Get("https://www7.whentowork.com/cgi-bin/w2wG4.dll/mgrschedule?" + sid + "&lmi=1&view=Pos&Day=" + weekday)
	if err != nil {
		fmt.Println(err)
		return nil, errors.New("failed to pull up schedules page")
	}
	defer response.Body.Close()

	// Read from response body
	body, err = ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, errors.New("failed to read response from when2work")
	}
	// fmt.Println(string(body))
	// This gets shifts via regex
	// either swl("952294753",2,"#000000","Brian Newman","959635624","07:00 - 14:20","   7.33 hours","OFF");
	// or ewl("959635634",2,"#000000","17:20 - 01:00","   7.67 hours","JPA"); if the shift is unassigned
	re = regexp.MustCompile(`\wwl\("\d+",\d,"#\w+","[\w\d -:,~><]+;`)
	shifts := re.FindAllString(string(body), -1)
	if shifts == nil {
		return nil, errors.New("failed to parse shifts")
	}

	// This gets the block numbers/shift and the respective number of shifts for that block
	// This if format sh(2,"[01]","3 shifts - 18.00 hours");
	// But only matching sh(2,"[01]","3
	// The [chararcters] may replaced with something else like Charter
	re = regexp.MustCompile(`sh\(\d+,"[\[\w+\]a-zA-Z ()]+","\d+`)
	blocks := re.FindAllString(string(body), -1)
	if blocks == nil {
		return nil, errors.New("failed to parse blocks")
	}

	// Keep track of position in shifts slice
	var position int
	// For each block, get the number of shifts under it, and loop that many positions in shifts array
	for _, block := range blocks {
		// If block is special or Charter(mini)/Charter(setra), get the number of shifts under it
		// Add this number to position variable so that those shifts are passed over in slice iteration below
		if strings.Contains(block, "Special") || strings.Contains(block, "Charter") {
			num := string(block[len(block)-1])
			incrementAmount, err := strconv.Atoi(num)
			if err != nil {
				fmt.Println(err)
				return nil, err
			}
			position += incrementAmount
			fmt.Println("Skipping charter or special shift")
			continue
		}
		// Get block, is in this format [BLOCK]
		re = regexp.MustCompile(`\[\w+\]`)
		// Turn block into string
		blockNum := string(re.Find([]byte(block)))
		// Replace surrounding []'s
		blockNum = blockNum[1 : len(blockNum)-1]
		// Get last number, aka number of shifts of this block
		// Is in this format "number
		re = regexp.MustCompile(`"\d+`)
		// Get value from find, []byte, turn it into a string, then remove the beginning "
		num, err := strconv.Atoi(string(re.Find([]byte(block))[1:]))
		if err != nil {
			fmt.Println(err)
			break
		}
		// Iterate a num number of times in shifts slice
		// Keeping track of indexing allows me to associate drivers to their blocks
		f := position + num
		for position < f {
			s := shifts[position]
			// If shift is unassigned skip
			// Only matching unassigned shifts because the number of shifts under a block include them
			if s[0] == 'e' {
				position++
				continue
			}
			// Get hex color
			re = regexp.MustCompile(`#\w+`)
			color := string(re.Find([]byte(s)))
			// Get driver name
			re = regexp.MustCompile(`"[A-z -]+",`)
			name := string(re.Find([]byte(s)))
			// Remove starting quote, comma, and ending quote
			name = name[1 : len(name)-2]
			// Get first and last name
			ns := strings.Split(name, " ")
			// Sanitize first name
			first := bm.Sanitize(ns[0])
			last := ""
			// Join indexes after 0 into last name string and sanitize
			if len(ns) > 1 {
				ns = append(ns[:0], ns[1:]...)
				last = bm.Sanitize(strings.Join(ns, " "))
			}
			// Get start and end time
			re = regexp.MustCompile(`\d{2}:\d{2}`)
			times := re.FindAllString(s, 2)
			// If time does not match export format, skip
			if len(times) != 2 {
				fmt.Println("Error getting time, skipping")
				position++
				continue
			}
			startTime := times[0]
			endTime := times[1]
			// Get location
			re = regexp.MustCompile(`[\w- \(\)\\*&@!#\+=_\{\}\[\]:';\.\?]+"\)`)
			// If location is missing, skip
			location := string(re.Find([]byte(s)))
			if len(location) < 3 {
				fmt.Println("Line has no location, skipping")
				position++
				continue
			}
			location = location[0:3]
			// Make sure location is capitalized
			location = strings.ToUpper(location)
			// Get current date
			date := time.Now().Format("2006-1-02")
			// If color does not start with f, ignore it, only looking for red, FF0000, and gold, ffcc00
			if color[1] == 'f' || color[1] == 'F' {
				// Remove '3' and convert color to lowercase
				color = strings.ToLower(color[1:])
				// If color is gold, Good dpm
				d := dpmDriver{
					FirstName: first,
					LastName:  last,
					Block:     bm.Sanitize(blockNum),
					Location:  bm.Sanitize(location),
					Date:      bm.Sanitize(date),
					StartTime: bm.Sanitize(startTime),
					EndTime:   bm.Sanitize(endTime),
				}
				// fmt.Println(d)
				if color == "ffcc00" {
					d.DPMType = "Type G: Good! (+1 Point)"
					d.Points = "+1"
					d.Notes = "Thanks!"
					dpms = append(dpms, d)
					// If color is red, DNS dpm
				} else if color == "ff0000" {
					d.DPMType = "Type D: DNS/Did Not Show (-10 Points)"
					d.Points = "-10"
					dpms = append(dpms, d)
				}
			}
			// Be sure to increment position
			position++
		}
	}
	return dpms, nil
}

// AutoSubmit takes in a slice of driver dpms and turns them into full dpms and submits them
// return an error if applicable otherwise returns nil
func autoSubmit(db *sqlx.DB, dpms []dpmDriver, sender int16) error {
	var (
		id     int16
		points int16
	)
	// Check to make sure that no dpms have be autosubmitted for today
	err := checkLastSubmission()
	if err != nil {
		return err
	}

	// Prepare select statement for repeated use
	stmt, err := db.Prepare(`SELECT id FROM users WHERE firstname=$1 AND lastname=$2 LIMIT 1`)
	if err != nil {
		fmt.Println("Problem with preparing SELECT statement.\n", err)
		return err
	}
	// Make sure to close stmt
	defer stmt.Close()
	// Query to create a new dpm into the database
	dpmIn := `INSERT INTO dpms (createid, userid, firstname, lastname, block, date, starttime, endtime, dpmtype, points, notes, created, location, approved) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, false)`
	// Iterate through the slice of dpms
	for _, d := range dpms {
		// Get id, and fulltimer bool based on first and last name
		err = stmt.QueryRow(d.FirstName, d.LastName).Scan(&id)
		// If error is not nil, assume it is becase user not in db, not fatal, keep going
		if err != nil {
			fmt.Println("Failed to find", d.FirstName, d.LastName, "in the DB")
			fmt.Println(err)
			continue
		}
		// Get current time
		created := time.Now().Format("2006-1-02 15:04:05")
		// If DPM is positive, can insert it into the db, no need to check fulltimer bool
		if d.DPMType == "Type G: Good! (+1 Point)" {
			// Set ponits
			points = 1
			// Execute query with values
			_, err := db.Exec(dpmIn, sender, id, d.FirstName, d.LastName, d.Block, d.Date, d.StartTime, d.EndTime, d.DPMType, points, d.Notes, created, d.Location)
			// If error, this is fatal so print and exit function
			if err != nil {
				fmt.Println("Autogen input failure, +1")
				fmt.Println(err)
				return err
			}
			// Create negative dpm
		} else if d.DPMType == "Type D: Preventable Accident 3,4 (-20 Points)" {
			// Set points
			points = -20
			// Execute query
			_, err := db.Exec(dpmIn, sender, id, d.FirstName, d.LastName, d.Block, d.Date, d.StartTime, d.EndTime, d.DPMType, points, d.Notes, created, d.Location)
			// If error, fatal, exit function
			if err != nil {
				fmt.Println("Autogen input failure, -20")
				fmt.Println(err)
				return err
			}
		}
	}
	// If function has made it this far with no errors, update submission time
	err = updateSubmitTime()
	if err != nil {
		return err
	}
	return nil
}

// checkLastSubmission checks text file to see when the last time autogen was called
// Only allows you autosubmit the dpms once a day
func checkLastSubmission() error {
	// Read from file
	readDate, err := ioutil.ReadFile("autogenTime.txt")
	// If err, assume it is because does not exist and move on
	if err != nil {
		fmt.Println("Missing text file")
		// Sanity check so if file is missing, the comparision/conversion of readDate does not panic
		readDate = []byte("")
	}
	// Get year, month, and day
	year, month, day := time.Now().Date()
	// Save these values into a string
	date := fmt.Sprintf("%v %s %v", year, month, day)
	// Check if date read from file is the same as the current date
	// If so, return error
	if date == string(readDate) {
		return errors.New("autosubmit has already been called for today")
	}
	return nil
}

func updateSubmitTime() error {
	// Get year, month, and day
	year, month, day := time.Now().Date()
	// Save these values into a string
	date := fmt.Sprintf("%v %s %v", year, month, day)
	// Write to file
	d1 := []byte(date)
	err := ioutil.WriteFile("autogenTime.txt", d1, 0644)
	if err != nil {
		fmt.Println(err)
		return errors.New("failed to write to file")
	}
	return nil
}
