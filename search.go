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

package zoekt

import (
	"bytes"
	"log"
	"sort"
)

var _ = log.Println

// contentProvider is an abstraction to treat matches for names and
// content with the same code.
type contentProvider struct {
	id    *indexData
	stats *Stats

	// mutable
	err      error
	idx      uint32
	_cb      []byte
	_data    []byte
	_nl      []uint32
	_nlBuf   []uint32
	_sects   []DocumentSection
	fileSize uint32
}

func (p *contentProvider) setDocument(docID uint32) {
	var fileStart uint32
	if docID > 0 {
		fileStart = p.id.fileEnds[docID-1]
	}

	p.idx = docID
	p.fileSize = p.id.fileEnds[docID] - fileStart

	p._nl = nil
	p._sects = nil
	p._data = nil
	p._cb = nil
}

func (p *contentProvider) docSections() []DocumentSection {
	if p._sects == nil {
		p._sects, p.err = p.id.readDocSections(p.idx)
	}
	return p._sects
}

func (p *contentProvider) newlines() []uint32 {
	if p._nl == nil {
		p._nl, p.err = p.id.readNewlines(p.idx, p._nlBuf)
		p._nlBuf = p._nl
	}
	return p._nl
}

func (p *contentProvider) data(fileName bool) []byte {
	if fileName {
		return p.id.fileNameContent[p.id.fileNameIndex[p.idx]:p.id.fileNameIndex[p.idx+1]]
	}

	if p._data == nil {
		p._data, p.err = p.id.readContents(p.idx)
		p.stats.FilesLoaded++
		p.stats.BytesLoaded += int64(len(p._data))
	}
	return p._data
}

func (p *contentProvider) caseBits(fileName bool) []byte {
	if fileName {
		return p.id.fileNameCaseBits[p.id.fileNameCaseBitsIndex[p.idx]:p.id.fileNameCaseBitsIndex[p.idx+1]]
	}

	if p._cb == nil {
		p._cb, p.err = p.id.readCaseBits(p.idx)
	}
	return p._cb
}

func (p *contentProvider) caseMatches(m *candidateMatch) bool {
	return m.caseMatches(p.caseBits(m.fileName))
}

func (p *contentProvider) matchContent(m *candidateMatch) bool {
	return m.matchContent(p.data(m.fileName))
}

func (p *contentProvider) fillMatches(ms []*candidateMatch) []Match {
	var result []Match
	if ms[0].fileName {
		// There is only "line" in a filename.
		res := Match{
			Line:     p.id.fileName(p.idx),
			FileName: true,
		}

		for _, m := range ms {
			res.Fragments = append(res.Fragments, MatchFragment{
				LineOff:     int(m.offset),
				MatchLength: int(m.matchSz),
				Offset:      m.offset,
			})

			result = []Match{res}
		}
	} else {
		result = p.fillContentMatches(ms)
	}

	sects := p.docSections()
	for i, m := range result {
		result[i].Score = matchScore(sects, &m)
	}

	return result
}

func (p *contentProvider) fillContentMatches(ms []*candidateMatch) []Match {
	var result []Match
	for len(ms) > 0 {
		m := ms[0]
		num, start, end := m.line(p.newlines(), p.fileSize)

		var lineCands []*candidateMatch

		endMatch := m.offset + m.matchSz
		for len(ms) > 0 {
			m := ms[0]
			if int(m.offset) < end {
				endMatch = m.offset + m.matchSz
				lineCands = append(lineCands, m)
				ms = ms[1:]
			} else {
				break
			}
		}

		data := p.data(false)

		// Due to merging matches, we may have a match that
		// crosses a line boundary. Prevent confusion by
		// taking lines until we pass the last match
		for end < len(data) && endMatch > uint32(end) {
			end = bytes.IndexByte(data[end+1:], '\n')
			if end == -1 {
				end = len(data)
			}
		}

		finalMatch := Match{
			LineStart: start,
			LineEnd:   end,
			LineNum:   num,
		}
		out := make([]byte, end-start+8)
		finalMatch.Line = toOriginal(out, p.data(false), p.caseBits(false), start, end)

		for _, m := range lineCands {
			finalMatch.Fragments = append(finalMatch.Fragments, MatchFragment{
				Offset:      m.offset,
				LineOff:     int(m.offset) - start,
				MatchLength: int(m.matchSz),
			})
		}

		result = append(result, finalMatch)
	}
	return result
}

const (
	// TODO - how to scale this relative to rank?
	scorePartialWordMatch   = 50.0
	scoreWordMatch          = 500.0
	scoreImportantThreshold = 2000.0
	scorePartialSymbol      = 4000.0
	scoreSymbol             = 7000.0
	scoreFactorAtomMatch    = 400.0
)

func findSection(secs []DocumentSection, off, sz uint32) *DocumentSection {
	j := sort.Search(len(secs), func(i int) bool {
		return secs[i].End >= off+sz
	})

	if j == len(secs) {
		return nil
	}

	if secs[j].Start <= off && off+sz <= secs[j].End {
		return &secs[j]
	}
	return nil
}

func matchScore(secs []DocumentSection, m *Match) float64 {
	var maxScore float64
	for _, f := range m.Fragments {
		startBoundary := f.LineOff < len(m.Line) && (f.LineOff == 0 || byteClass(m.Line[f.LineOff-1]) != byteClass(m.Line[f.LineOff]))

		end := int(f.LineOff) + f.MatchLength
		endBoundary := end > 0 && (end == len(m.Line) || byteClass(m.Line[end-1]) != byteClass(m.Line[end]))

		score := 0.0
		if startBoundary && endBoundary {
			score = scoreWordMatch
		} else if startBoundary || endBoundary {
			score = scorePartialWordMatch
		}

		sec := findSection(secs, f.Offset, uint32(f.MatchLength))
		if sec != nil {
			startMatch := sec.Start == f.Offset
			endMatch := sec.End == f.Offset+uint32(f.MatchLength)
			if startMatch && endMatch {
				score += scoreSymbol
			} else if startMatch || endMatch {
				score += (scoreSymbol + scorePartialSymbol) / 2
			} else {
				score += scorePartialSymbol
			}
		}
		if score > maxScore {
			maxScore = score
		}
	}
	return maxScore
}

type matchScoreSlice []Match

func (m matchScoreSlice) Len() int           { return len(m) }
func (m matchScoreSlice) Swap(i, j int)      { m[i], m[j] = m[j], m[i] }
func (m matchScoreSlice) Less(i, j int) bool { return m[i].Score > m[j].Score }

type fileMatchSlice []FileMatch

func (m fileMatchSlice) Len() int           { return len(m) }
func (m fileMatchSlice) Swap(i, j int)      { m[i], m[j] = m[j], m[i] }
func (m fileMatchSlice) Less(i, j int) bool { return m[i].Score > m[j].Score }

func sortMatchesByScore(ms []Match) {
	sort.Sort(matchScoreSlice(ms))
}

func sortFilesByScore(ms []FileMatch) {
	sort.Sort(fileMatchSlice(ms))
}
