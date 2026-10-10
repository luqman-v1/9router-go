package translator

// StripContentTypes removes content parts a model cannot accept — port of
// stripContentTypes in open-sse/translator/index.js:37. An emptied content
// array becomes "". Bodies without a messages array are untouched.
func StripContentTypes(body map[string]any, strip []string) {
	if len(strip) == 0 {
		return
	}
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}
	stripImage, stripAudio := false, false
	for _, s := range strip {
		switch s {
		case "image":
			stripImage = true
		case "audio":
			stripAudio = true
		}
	}
	if !stripImage && !stripAudio {
		return
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		kept := parts[:0]
		for _, p := range parts {
			block, ok := p.(map[string]any)
			if !ok {
				kept = append(kept, p)
				continue
			}
			typ, _ := block["type"].(string)
			switch typ {
			case openaiBlockImageURL, openaiBlockImage:
				if stripImage {
					continue
				}
			case openaiBlockAudioURL, openaiBlockInputAudio:
				if stripAudio {
					continue
				}
			}
			kept = append(kept, p)
		}
		if len(kept) == 0 {
			msg["content"] = ""
		} else {
			msg["content"] = kept
		}
	}
}
