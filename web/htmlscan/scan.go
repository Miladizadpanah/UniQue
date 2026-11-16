// Package htmlscan inspects a rendered panel page for markup that Vue will
// never compile.
//
// Every panel page mounts one Vue root on #app and Vue compiles only that
// subtree. Markup emitted after `page/shell_end` therefore reaches the browser
// verbatim: `[[ n.name ]]` stays those nine literal characters and any
// v-model/@click in it is inert. Files under web/html/modals are fine — each
// carries its own `new Vue({ el: '#modal-id' })` — but an inline <a-modal>
// dropped after shell_end is a silently dead control. That is exactly how the
// clone-to-node dialog shipped: working API, static dialog, no build error.
//
// The package is pure parsing so the same code can be exercised in a unit test
// on any platform, and against real rendered pages by the page render test.
package htmlscan

import (
	"regexp"
	"strings"
)

var (
	scriptTag = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	divTag    = regexp.MustCompile(`<div\b|</div>`)
	modalTag  = regexp.MustCompile(`(?is)<a-modal\b([^>]*)>|</a-modal>`)
)

// ModalsOutsideApp returns the attribute text of every <a-modal> that starts
// after #app's closing </div>, excluding ones nested inside another such modal
// (those are compiled by the outer modal's own Vue root).
//
// The second result is the concatenation of the page's script bodies, which is
// where a self-mount lives. Script bodies are removed from the markup before
// scanning because page scripts contain HTML strings that would otherwise be
// read as tags.
func ModalsOutsideApp(page string) (modals []string, scripts string) {
	var scriptBodies strings.Builder
	markup := scriptTag.ReplaceAllStringFunc(page, func(tag string) string {
		if i := strings.Index(tag, ">"); i >= 0 {
			scriptBodies.WriteString(tag[i+1:])
		}
		return ""
	})

	app := strings.Index(markup, `id="app"`)
	if app < 0 {
		return nil, scriptBodies.String()
	}

	// Walk the div depth from #app's own <div> to its matching </div>. Nothing
	// else delimits the Vue root in the served HTML.
	depth, end := 0, -1
	for _, loc := range divTag.FindAllStringIndex(markup[app:], -1) {
		if markup[app+loc[0]:app+loc[1]] == "</div>" {
			depth--
			if depth == 0 {
				end = app + loc[1]
				break
			}
			continue
		}
		depth++
	}
	if end < 0 {
		return nil, scriptBodies.String()
	}

	nesting := 0
	for _, loc := range modalTag.FindAllStringSubmatchIndex(markup[end:], -1) {
		tag := markup[end+loc[0] : end+loc[1]]
		if strings.HasPrefix(tag, "</") {
			if nesting > 0 {
				nesting--
			}
			continue
		}
		attrs := markup[end+loc[2] : end+loc[3]]
		selfClosing := strings.HasSuffix(strings.TrimSpace(attrs), "/")
		if nesting == 0 && !selfClosing {
			modals = append(modals, attrs)
		}
		if !selfClosing {
			nesting++
		}
	}
	return modals, scriptBodies.String()
}

// MountsID reports whether the given script text mounts a Vue root on #id, in
// any of the spellings this repo uses (`el: '#x'`, `el:'#x'`, double quotes).
func MountsID(scripts, id string) bool {
	if id == "" {
		return false
	}
	for _, quote := range []string{"'", `"`} {
		if strings.Contains(scripts, "el:"+quote+"#"+id+quote) ||
			strings.Contains(scripts, "el: "+quote+"#"+id+quote) {
			return true
		}
	}
	return false
}

// AttrValue pulls a double-quoted attribute out of a tag's attribute text.
// Panel templates always use double quotes.
func AttrValue(attrs, name string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(attrs); m != nil {
		return m[1]
	}
	return ""
}

// Condense collapses a multi-line tag into one readable line for messages.
func Condense(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
