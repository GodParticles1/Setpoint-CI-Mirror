package nginxbaseline

func httpEvaluationContexts(configuration parsedConfig) [][]blockContext {
	result := make([][]blockContext, 0)
	for _, current := range configuration.Blocks {
		context := fullBlockContext(current)
		if !contextStartsWith(context, "http") {
			continue
		}
		if current.Frame.Name == "server" || current.Frame.Name == "location" {
			result = append(result, context)
		}
	}
	return result
}

func directivesAt(values []directive, name string, context []blockContext) []directive {
	result := make([]directive, 0)
	for _, current := range values {
		if current.Name == name && sameContext(current.Context, context) {
			result = append(result, current)
		}
	}
	return result
}

func effectiveDirectives(values []directive, name string, context []blockContext) []directive {
	for length := len(context); length >= 1; length-- {
		if result := directivesAt(values, name, context[:length]); len(result) > 0 {
			return result
		}
	}
	return nil
}

func httpConfigAmbiguous(configuration parsedConfig) bool {
	if !configuration.Complete {
		return true
	}
	for _, current := range configuration.Directives {
		if current.Name == "include" && (len(current.Context) == 0 || contextStartsWith(current.Context, "http")) {
			return true
		}
	}
	for _, current := range configuration.Blocks {
		context := fullBlockContext(current)
		if contextStartsWith(context, "stream") {
			continue
		}
		if (current.Frame.Name == "server" || current.Frame.Name == "location") && !contextStartsWith(context, "http") {
			return true
		}
	}
	return false
}

func innermostContext(context []blockContext, name string) (blockContext, bool) {
	for index := len(context) - 1; index >= 0; index-- {
		if context[index].Name == name {
			return context[index], true
		}
	}
	return blockContext{}, false
}
