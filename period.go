package main

import (
	"fmt"
	"time"
)

type YearMonth struct {
	Year  int
	Month int
}

func (ym YearMonth) DHIS2Period() string {
	return fmt.Sprintf("%d%02d", ym.Year, ym.Month)
}

func (ym YearMonth) String() string {
	return fmt.Sprintf("%d-%02d", ym.Year, ym.Month)
}

// GenerateMonthPeriods returns the last n months (excluding current month).
func GenerateMonthPeriods(n int) []YearMonth {
	now := time.Now().UTC()
	periods := make([]YearMonth, 0, n)

	for i := n; i >= 1; i-- {
		d := now.AddDate(0, -i, 0)
		periods = append(periods, YearMonth{Year: d.Year(), Month: int(d.Month())})
	}

	return periods
}
