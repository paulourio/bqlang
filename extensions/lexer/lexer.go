// Code generated by gocc; DO NOT EDIT.

package lexer

import (
	"io/ioutil"
	"unicode/utf8"

	"github.com/paulourio/bqlang/extensions/token"
)

const (
	NoState    = -1
	NumStates  = 187
	NumSymbols = 167
)

type Lexer struct {
	src     []byte
	pos     int
	line    int
	column  int
	Context token.Context
}

func NewLexer(src []byte) *Lexer {
	lexer := &Lexer{
		src:     src,
		pos:     0,
		line:    1,
		column:  1,
		Context: nil,
	}
	return lexer
}

// SourceContext is a simple instance of a token.Context which
// contains the name of the source file.
type SourceContext struct {
	Filepath string
}

func (s *SourceContext) Source() string {
	return s.Filepath
}

func NewLexerFile(fpath string) (*Lexer, error) {
	src, err := ioutil.ReadFile(fpath)
	if err != nil {
		return nil, err
	}
	lexer := NewLexer(src)
	lexer.Context = &SourceContext{Filepath: fpath}
	return lexer, nil
}

func (l *Lexer) Scan() (tok *token.Token) {
	tok = &token.Token{}
	if l.pos >= len(l.src) {
		tok.Type = token.EOF
		tok.Pos.Offset, tok.Pos.Line, tok.Pos.Column = l.pos, l.line, l.column
		tok.Pos.Context = l.Context
		return
	}
	start, startLine, startColumn, end := l.pos, l.line, l.column, 0
	tok.Type = token.INVALID
	state, rune1, size := 0, rune(-1), 0
	for state != -1 {
		if l.pos >= len(l.src) {
			rune1 = -1
		} else {
			rune1, size = utf8.DecodeRune(l.src[l.pos:])
			l.pos += size
		}

		nextState := -1
		if rune1 != -1 {
			nextState = TransTab[state](rune1)
		}
		state = nextState

		if state != -1 {

			switch rune1 {
			case '\n':
				l.line++
				l.column = 1
			case '\r':
				l.column = 1
			case '\t':
				l.column += 4
			default:
				l.column++
			}

			switch {
			case ActTab[state].Accept != -1:
				tok.Type = ActTab[state].Accept
				end = l.pos
			case ActTab[state].Ignore != "":
				start, startLine, startColumn = l.pos, l.line, l.column
				state = 0
				if start >= len(l.src) {
					tok.Type = token.EOF
				}

			}
		} else {
			if tok.Type == token.INVALID {
				end = l.pos
			}
		}
	}
	if end > start {
		l.pos = end
		tok.Lit = l.src[start:end]
	} else {
		tok.Lit = []byte{}
	}
	tok.Pos.Offset, tok.Pos.Line, tok.Pos.Column = start, startLine, startColumn
	tok.Pos.Context = l.Context

	return
}

func (l *Lexer) Reset() {
	l.pos = 0
}

/*
Lexer symbols:
0: '-'
1: '-'
2: '\n'
3: '#'
4: '\n'
5: '/'
6: '/'
7: '\n'
8: '\n'
9: '{'
10: '%'
11: '-'
12: '-'
13: '%'
14: '}'
15: '-'
16: '#'
17: '}'
18: '{'
19: '{'
20: '-'
21: '-'
22: '}'
23: '}'
24: '*'
25: '<'
26: '>'
27: '['
28: ']'
29: '('
30: ')'
31: ','
32: '.'
33: '|'
34: '^'
35: '&'
36: '<'
37: '<'
38: '>'
39: '>'
40: '='
41: '>'
42: '/'
43: '+'
44: '-'
45: '|'
46: '|'
47: '<'
48: '>'
49: '!'
50: '='
51: '='
52: '<'
53: '='
54: '>'
55: '='
56: ';'
57: '@'
58: '{'
59: '}'
60: '~'
61: 'e'
62: 'l'
63: 's'
64: 'e'
65: 'i'
66: 'f'
67: 'f'
68: 'o'
69: 'r'
70: 'e'
71: 'n'
72: 'd'
73: 'f'
74: 'o'
75: 'r'
76: 'e'
77: 'l'
78: 'i'
79: 'f'
80: 'e'
81: 'n'
82: 'd'
83: 'i'
84: 'f'
85: 's'
86: 'e'
87: 't'
88: '?'
89: '\'
90: '"'
91: '''
92: '\'
93: '\n'
94: '\r'
95: 'r'
96: 'R'
97: 'r'
98: 'R'
99: 'b'
100: 'B'
101: 'b'
102: 'B'
103: 'r'
104: 'R'
105: 'b'
106: 'B'
107: '"'
108: '''
109: '\n'
110: '\n'
111: '''
112: '"'
113: '*'
114: '*'
115: '/'
116: '/'
117: '*'
118: '*'
119: '{'
120: '#'
121: '-'
122: '-'
123: '#'
124: '}'
125: '.'
126: '_'
127: '`'
128: '.'
129: '.'
130: 'e'
131: 'E'
132: '+'
133: '-'
134: '0'
135: 'x'
136: 'X'
137: ' '
138: '\t'
139: '\r'
140: \u00a0
141: \u2000
142: \u2001
143: \u2003
144: \u2004
145: \u0001-'\t'
146: '\v'-'!'
147: '#'-'&'
148: '('-'['
149: ']'-\u007f
150: \u0080-\ufffc
151: \ufffe-\U0010ffff
152: \u0001-'\t'
153: '\v'-\u007f
154: \u0080-\ufffc
155: \ufffe-\U0010ffff
156: '0'-'9'
157: 'a'-'z'
158: 'A'-'Z'
159: \u0001-'\t'
160: '\v'-'['
161: ']'-'_'
162: 'a'-\u007f
163: '0'-'9'
164: 'a'-'f'
165: 'A'-'F'
166: .
*/