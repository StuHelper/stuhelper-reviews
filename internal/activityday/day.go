// Package activityday defines the business calendar for discovery activity.
package activityday

import (
	"time"
	_ "time/tzdata"
)

const Zone = "Asia/Shanghai"

var Location = loadLocation()

func loadLocation() *time.Location {
	location, err := time.LoadLocation(Zone)
	if err != nil {
		panic(err)
	}
	return location
}

// Date returns a PostgreSQL date label encoded at UTC midnight.
func Date(at time.Time) time.Time {
	year, month, day := at.In(Location).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
