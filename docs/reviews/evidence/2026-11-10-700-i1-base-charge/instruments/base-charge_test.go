// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
)

func TestMainEmitsEnvironmentAndAllSamples(t *testing.T) {
	originalCounts := measurementLeaseCounts
	originalRepetitions := measurementRepetitions
	measurementLeaseCounts = []int{0, 1}
	measurementRepetitions = 1
	t.Cleanup(func() {
		measurementLeaseCounts = originalCounts
		measurementRepetitions = originalRepetitions
	})

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = originalStdout })
	main()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = originalStdout

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	rows := 0
	for scanner.Scan() {
		var row result
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("measurement row %d is invalid JSON: %v", rows+1, err)
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Fatalf("measurement emitted %d rows, want environment + two samples", rows)
	}
}

func TestRunReturnsWriterErrors(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		writer := &failingWriter{failAt: failAt}
		if err := run(writer, []int{0}, 1); !errors.Is(err, errWriteFailed) {
			t.Fatalf("run with write failure %d returned %v, want errWriteFailed", failAt, err)
		}
	}
}

var errWriteFailed = errors.New("injected write failure")

type failingWriter struct {
	writes int
	failAt int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errWriteFailed
	}
	return len(p), nil
}
