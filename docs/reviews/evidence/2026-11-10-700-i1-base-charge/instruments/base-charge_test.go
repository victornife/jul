// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestMainEmitsEnvironmentAndAllSamples(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	main()
	if err := writer.Close(); err != nil {
		os.Stdout = originalStdout
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
	if rows != 29 {
		t.Fatalf("measurement emitted %d rows, want environment + 28 samples", rows)
	}
}
