// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package codesearch

import (
	"bytes"
	"log"
	"sort"
)

var _ = log.Println

type docIterator struct {
	query  *SubstringQuery

	patLen   uint32
	first []uint32
	last  []uint32

	fileIdx int
	ends  []uint32
}

type candidateMatch struct {
	query  *SubstringQuery

	substrBytes []byte
	lowered     []byte
	caseMask [][]byte
	caseBits [][]byte

	file   uint32
	offset uint32
}

func (m *candidateMatch) populateCaseBits() {
	if !m.query.CaseSensitive || m.caseMask != nil {
		return
	}
	m.caseMask, m.caseBits = findCaseMasks(m.substrBytes)
}

func (m *candidateMatch) caseMatches(fileCaseBits []byte) bool {
	if !m.query.CaseSensitive {
		return true
	}
	m.populateCaseBits()
	patLen := len(m.substrBytes)
	startExtend := m.offset % 8
	patEnd := m.offset + uint32(patLen)
	endExtend := (8 - (patEnd % 8)) % 8

	start := m.offset - startExtend
	end := m.offset + uint32(patLen) + endExtend

	fileBits := append([]byte{}, fileCaseBits[start/8:end/8]...)
	mask := m.caseMask[startExtend]
	bits := m.caseBits[startExtend]

	for i := range fileBits {
		if fileBits[i]&mask[i] != bits[i] {
			return false
		}
	}

	return true
}

func (m *candidateMatch) matchContent(content []byte) bool {
	return bytes.Compare(content[m.offset:m.offset+uint32(len(m.lowered))], m.lowered) == 0
}

func (m *candidateMatch) line(newlines []uint32, content []byte, caseBits []byte) (lineNum, lineOff int, lineContent []byte) {
	idx := sort.Search(len(newlines), func(n int) bool {
		return newlines[n] >= m.offset
	})

	end := len(content)
	if idx < len(newlines) {
		end = int(newlines[idx])
	}

	start := 0
	if idx > 0 {
		start = int(newlines[idx-1] + 1)
	}

	return idx + 1, int(m.offset) - start, toOriginal(content, caseBits, start, end)
}

func (s *docIterator) next() []candidateMatch {
	patBytes := []byte(s.query.Pattern)
	lowerPatBytes := toLower(patBytes)

	distance := s.patLen - NGRAM

	var candidates []candidateMatch
	for {
		if len(s.first) == 0 || len(s.last) == 0 {
			break
		}
		p1 := s.first[0]
		p2 := s.last[0]

		for s.fileIdx < len(s.ends) && s.ends[s.fileIdx] <= p1 {
			s.fileIdx++
		}

		if p1+distance < p2 {
			s.first = s.first[1:]
		} else if p1+distance > p2 {
			s.last = s.last[1:]
		} else {
			s.first = s.first[1:]
			s.last = s.last[1:]

			if p1+uint32(s.patLen) >= s.ends[s.fileIdx] {
				continue
			}

			fileStart := uint32(0)
			if s.fileIdx > 0 {
				fileStart += s.ends[s.fileIdx-1]
			}
			candidates = append(candidates,
				candidateMatch{
					query: s.query,
					substrBytes: patBytes,
					lowered: lowerPatBytes,
					file: uint32(s.fileIdx),
					offset: p1 - fileStart,
				})
		}
	}
	return candidates
}