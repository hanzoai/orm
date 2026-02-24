package json

import (
	"bufio"
	"io"
	"log"
	"os"
)

// Record holds a line of NDJSON data.
type Record struct {
	Data  string
	Index int
}

// Iterator reads an NDJSON file and returns records via channel.
func Iterator(filename string) <-chan Record {
	ch := make(chan Record)

	go func() {
		file, err := os.Open(filename)
		if err != nil {
			log.Fatalf("Failed to open JSON file: %v", err)
		}
		defer file.Close()

		r := bufio.NewReader(file)
		line := 1
		for {
			data, err := r.ReadString(10) // 0x0A separator = newline
			ch <- Record{data, line}
			if err == io.EOF {
				break
			} else if err != nil {
				log.Fatalf("Failed to read JSON file: %v", err)
			}
			line++
		}
		close(ch)
	}()

	return ch
}
