package nginxbaseline

import "strings"

type blockContext struct {
	ID   int
	Name string
	Args []string
}

type directive struct {
	Name    string
	Args    []string
	Context []blockContext
}

type block struct {
	Frame   blockContext
	Parents []blockContext
}

type parsedConfig struct {
	Directives []directive
	Blocks     []block
	Complete   bool
}

func parseConfig(contents string) parsedConfig {
	tokens, tokenComplete := tokenize(contents)
	contextStack := []blockContext{}
	pending := []string{}
	directives := []directive{}
	blocks := []block{}
	complete := tokenComplete
	nextBlockID := 1
	for _, token := range tokens {
		switch token {
		case ";":
			if len(pending) > 0 {
				directives = append(directives, directive{
					Name: strings.ToLower(pending[0]), Args: append([]string(nil), pending[1:]...),
					Context: cloneContext(contextStack),
				})
			}
			pending = pending[:0]
		case "{":
			if len(pending) == 0 {
				complete = false
				continue
			}
			frame := blockContext{ID: nextBlockID, Name: strings.ToLower(pending[0]), Args: append([]string(nil), pending[1:]...)}
			nextBlockID++
			blocks = append(blocks, block{Frame: frame, Parents: cloneContext(contextStack)})
			contextStack = append(contextStack, frame)
			pending = pending[:0]
		case "}":
			pending = pending[:0]
			if len(contextStack) > 0 {
				contextStack = contextStack[:len(contextStack)-1]
			} else {
				complete = false
			}
		default:
			pending = append(pending, token)
		}
	}
	if len(contextStack) != 0 || len(pending) != 0 {
		complete = false
	}
	return parsedConfig{Directives: directives, Blocks: blocks, Complete: complete}
}

func tokenize(contents string) ([]string, bool) {
	tokens := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	comment := false
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, character := range contents {
		if comment {
			if character == '\n' {
				comment = false
			}
			continue
		}
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
		case '#':
			flush()
			comment = true
		case '{', '}', ';':
			flush()
			tokens = append(tokens, string(character))
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			current.WriteRune(character)
		}
	}
	flush()
	return tokens, quote == 0 && !escaped
}

func findDirectives(directives []directive, name string) []directive {
	result := []directive{}
	for _, current := range directives {
		if current.Name == name {
			result = append(result, current)
		}
	}
	return result
}

func joinedArgs(values []directive) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strings.Join(value.Args, " "))
	}
	return strings.Join(parts, " | ")
}

func cloneContext(values []blockContext) []blockContext {
	result := make([]blockContext, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Args = append([]string(nil), value.Args...)
	}
	return result
}

func fullBlockContext(value block) []blockContext {
	result := cloneContext(value.Parents)
	return append(result, value.Frame)
}

func sameContext(left, right []blockContext) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID {
			return false
		}
	}
	return true
}

func contextStartsWith(value []blockContext, name string) bool {
	return len(value) > 0 && value[0].Name == name
}
