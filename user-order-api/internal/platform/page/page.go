package page

type Request struct {
	Limit   int
	AfterID int64
}

type Result[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
