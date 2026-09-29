package parse

import "testing"

func TestHTMLToText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"inline elements", "<b>Hello</b> <i>wor</i>ld", "Hello world"},
		{"paragraphs", "<p>One</p><p>Two</p>", "One\n\nTwo"},
		{"br", "a<br>b<br/>c", "a\nb\nc"},
		{"divs", "<div>a</div><div>b</div>", "a\nb"},
		{"gmail empty line", "<div>a</div><div><br></div><div>b</div>", "a\n\nb"},
		{"script and style dropped", "x<script>var a='<p>no</p>';</script><style>p{color:red}</style>y", "xy"},
		{"title dropped", "<html><head><title>Secret</title></head><body>Body</body></html>", "Body"},
		{"comments and conditional comments dropped", "a<!-- c --><!--[if mso]><p>mso</p><![endif]-->b", "ab"},
		{"entities", "a&nbsp;b &amp; c &lt;d&gt; &euro;", "a b & c <d> €"},
		{"whitespace collapsed", "a \n\t  b    c", "a b c"},
		{"list", "<ul><li>one</li><li>two</li></ul>", "one\ntwo"},
		{"table cells", "<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>", "a b\nc d"},
		{"blockquote", "<p>reply</p><blockquote>quoted</blockquote>", "reply\n\nquoted"},
		{"preheader zero-width padding", "Hi\u200c\u00a0\u200c\u00a0there", "Hi there"},
		{"unclosed tags", "<div><p>a<b>b", "ab"},
		{"attribute text is not content", `<a href="http://x.example" title="t">link</a><img alt="pic" src="cid:x">`, "link"},
		{"unclosed script hides rest", "a<script>b", "a"},
		{"plain text passes", "just text", "just text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "text", htmlToText(tt.in), tt.want)
		})
	}
}

func TestUnflow(t *testing.T) {
	tests := []struct {
		name, in string
		delSP    bool
		want     string
	}{
		{"fixed lines untouched", "a\nb\nc", false, "a\nb\nc"},
		{"soft break", "hello \nworld", false, "hello world"},
		{"soft break delsp", "hello \nworld", true, "helloworld"},
		{"three lines", "a \nb \nc\nd", false, "a b c\nd"},
		{"signature separator is not flowed", "text\n-- \nname", false, "text\n-- \nname"},
		{"space stuffing removed", "a\n From here\nb", false, "a\nFrom here\nb"},
		{"quoted lines join within depth", "> a \n> b\nc", false, "> a b\nc"},
		{"depth change ends line", "> a \nb", false, "> a \nb"},
		{"nested quotes", ">> a \n>> b", false, ">> a b"},
		{"empty quoted line", ">\n> x", false, ">\n> x"},
		{"trailing soft break at end", "a ", false, "a "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "text", unflow(tt.in, tt.delSP), tt.want)
		})
	}
}
