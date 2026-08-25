// Package pagination parses page/limit inputs and builds response metadata.
// It takes plain strings so it stays transport agnostic; the Fiber binding
// lives in internal/shared/httpx.
package pagination

import "strconv"

const (
	// DefaultPage is used when the caller sends no or an invalid page.
	DefaultPage = 1
	// DefaultLimit is used when the caller sends no or an invalid limit.
	DefaultLimit = 20
	// MaxLimit caps how many rows a single page may request.
	MaxLimit = 100
)

// Params is a sanitised page request.
type Params struct {
	Page  int `json:"page"`
	Limit int `json:"limit"`
}

// Parse converts raw query values into safe Params. Invalid or out of range
// values silently fall back to the defaults, which keeps handlers simple.
func Parse(rawPage, rawLimit string) Params {
	page, err := strconv.Atoi(rawPage)
	if err != nil || page < 1 {
		page = DefaultPage
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil || limit < 1 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return Params{Page: page, Limit: limit}
}

// Offset is the SQL OFFSET matching the params.
func (p Params) Offset() int { return (p.Page - 1) * p.Limit }

// Meta is the pagination block attached to list responses.
type Meta struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// NewMeta builds the response metadata for a given page and total row count.
func NewMeta(p Params, total int64) Meta {
	totalPages := 0
	if p.Limit > 0 && total > 0 {
		totalPages = int((total + int64(p.Limit) - 1) / int64(p.Limit))
	}
	return Meta{Page: p.Page, Limit: p.Limit, Total: total, TotalPages: totalPages}
}
