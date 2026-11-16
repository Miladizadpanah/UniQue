package htmlscan

import "testing"

// A page shaped like the real ones: #app with nested divs, then page scripts,
// then the modals block, then inline JS.
func page(modals string) string {
	return `<div id="app" class="bo-shell">
  <div class="bo-main">
    <main class="bo-content">
      <div class="bo-card">content</div>
    </main>
  </div>
</div>
<script src="/assets/vue.min.js"></script>
<script>
  const app = new Vue({ el: '#app', data: { x: 1 }, methods: { y() { return "<div>not markup</div>"; } } });
</script>
` + modals
}

func TestInlineModalOutsideAppIsReported(t *testing.T) {
	// The regression: an inline dialog after shell_end, with no self-mount.
	src := page(`<a-modal v-model="clone.visible" :width="480">
  <a-select-option v-for="n in clone.nodes" :key="n.id">[[ n.name ]]</a-select-option>
</a-modal>`)

	modals, scripts := ModalsOutsideApp(src)
	if len(modals) != 1 {
		t.Fatalf("expected 1 modal outside #app, got %d", len(modals))
	}
	if id := AttrValue(modals[0], "id"); id != "" {
		t.Fatalf("expected no id on the inline modal, got %q", id)
	}
	if MountsID(scripts, "") {
		t.Fatal("empty id must never count as mounted")
	}
}

func TestSelfMountedModalOutsideAppIsAccepted(t *testing.T) {
	// The established pattern: modals/*.html carry their own root.
	src := page(`<a-modal id="clone-modal" v-model="clone.visible">
  <span>[[ clone.label ]]</span>
</a-modal>
<script>
  const cloneModalApp = new Vue({ delimiters: ['[[', ']]'], el: '#clone-modal', data: { clone: {} } });
</script>`)

	modals, scripts := ModalsOutsideApp(src)
	if len(modals) != 1 {
		t.Fatalf("expected 1 modal outside #app, got %d", len(modals))
	}
	id := AttrValue(modals[0], "id")
	if id != "clone-modal" {
		t.Fatalf("expected id clone-modal, got %q", id)
	}
	if !MountsID(scripts, id) {
		t.Fatalf("expected %q to be recognised as mounted", id)
	}
	if MountsID(scripts, "other-modal") {
		t.Fatal("a mount for another id must not satisfy this modal")
	}
}

func TestModalInsideAppIsNotReported(t *testing.T) {
	src := `<div id="app">
  <div class="bo-main">
    <a-modal v-model="clone.visible"><span>[[ clone.label ]]</span></a-modal>
  </div>
</div>
<script>new Vue({ el: '#app' });</script>`

	modals, _ := ModalsOutsideApp(src)
	if len(modals) != 0 {
		t.Fatalf("modal inside #app must not be reported, got %d: %v", len(modals), modals)
	}
}

func TestNestedModalIsNotReported(t *testing.T) {
	// Zoom dialogs live inside a self-mounted modal and are compiled by it.
	src := page(`<a-modal id="info-modal" v-model="info.visible">
  <a-modal id="info-zoom" v-model="info.zoom"></a-modal>
</a-modal>
<script>new Vue({ el: '#info-modal' });</script>`)

	modals, _ := ModalsOutsideApp(src)
	if len(modals) != 1 {
		t.Fatalf("expected only the outer modal to be reported, got %d", len(modals))
	}
	if id := AttrValue(modals[0], "id"); id != "info-modal" {
		t.Fatalf("expected info-modal, got %q", id)
	}
}

func TestScriptMarkupDoesNotConfuseScan(t *testing.T) {
	// Vue render functions in page JS contain <div> strings; the div counter
	// must not see them, or #app's end lands in the wrong place.
	src := page(`<a-modal id="m" v-model="m.visible"></a-modal>
<script>
  app.$confirm({ content: (h) => h('div', [h('div', 'x')]) });
  new Vue({ el: '#m' });
</script>`)

	modals, scripts := ModalsOutsideApp(src)
	if len(modals) != 1 {
		t.Fatalf("expected 1 modal, got %d", len(modals))
	}
	if !MountsID(scripts, AttrValue(modals[0], "id")) {
		t.Fatal("mount inside a script with render-function markup must still be found")
	}
}
