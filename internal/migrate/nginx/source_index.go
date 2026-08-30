// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"unicode"
)

type indexedDirective struct {
	Name  string
	Start AssessmentPosition
	End   AssessmentPosition
}

// indexSourceDirectives is a lexical position index, not a second NGINX parser.
// It records directive names and statement/block boundaries while deliberately
// ignoring parameter semantics. Any ambiguous construct can fall back to the
// line-only coordinates already supplied by gonginx.
func indexSourceDirectives(data []byte) []indexedDirective {
	s := sourceIndexer{data: data, line: 1, column: 1, expectDirective: true}
	s.scan()
	return s.items
}

type sourceIndexer struct {
	data            []byte
	offset          int
	line            int
	column          int
	expectDirective bool
	items           []indexedDirective
	openBlocks      []int
	current         int
}

func (s *sourceIndexer) scan() {
	s.current = -1
	for s.offset < len(s.data) {
		if s.skipWhitespaceOrComment() {
			continue
		}
		ch := s.data[s.offset]
		switch ch {
		case '}':
			pos := s.position()
			s.advanceByte(ch)
			if len(s.openBlocks) > 0 {
				idx := s.openBlocks[len(s.openBlocks)-1]
				s.openBlocks = s.openBlocks[:len(s.openBlocks)-1]
				s.items[idx].End = pos
			}
			s.current = -1
			s.expectDirective = true
			continue
		case ';':
			pos := s.position()
			s.advanceByte(ch)
			if s.current >= 0 && s.current < len(s.items) {
				s.items[s.current].End = pos
			}
			s.current = -1
			s.expectDirective = true
			continue
		case '{':
			pos := s.position()
			s.advanceByte(ch)
			if s.current >= 0 && s.current < len(s.items) {
				s.items[s.current].End = pos
				s.openBlocks = append(s.openBlocks, s.current)
			}
			s.current = -1
			s.expectDirective = true
			continue
		case '\'', '"':
			s.skipQuoted(ch)
			s.expectDirective = false
			continue
		}

		if s.expectDirective {
			start := s.position()
			name := s.readWord()
			if name != "" {
				s.items = append(s.items, indexedDirective{Name: name, Start: start})
				s.current = len(s.items) - 1
				s.expectDirective = false
				continue
			}
		}
		s.advanceByte(ch)
	}
}

func (s *sourceIndexer) skipWhitespaceOrComment() bool {
	if s.offset >= len(s.data) {
		return false
	}
	ch := s.data[s.offset]
	if unicode.IsSpace(rune(ch)) {
		s.advanceByte(ch)
		return true
	}
	if ch != '#' {
		return false
	}
	for s.offset < len(s.data) {
		ch = s.data[s.offset]
		s.advanceByte(ch)
		if ch == '\n' {
			break
		}
	}
	return true
}

func (s *sourceIndexer) skipQuoted(quote byte) {
	s.advanceByte(quote)
	escaped := false
	for s.offset < len(s.data) {
		ch := s.data[s.offset]
		s.advanceByte(ch)
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == quote {
			return
		}
	}
}

func (s *sourceIndexer) readWord() string {
	start := s.offset
	for s.offset < len(s.data) {
		ch := s.data[s.offset]
		if unicode.IsSpace(rune(ch)) || strings.ContainsRune(";{}#\"'", rune(ch)) {
			break
		}
		s.advanceByte(ch)
	}
	return string(s.data[start:s.offset])
}

func (s *sourceIndexer) position() AssessmentPosition {
	return AssessmentPosition{Line: s.line, Column: s.column}
}

func (s *sourceIndexer) advanceByte(ch byte) {
	s.offset++
	if ch == '\n' {
		s.line++
		s.column = 1
		return
	}
	// Treat CRLF as one line break. A lone CR remains one printable column;
	// the following LF performs the actual line increment.
	s.column++
}
