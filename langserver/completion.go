package langserver

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/sourcegraph/go-langserver/langserver/internal/gocode"
	"github.com/sourcegraph/go-langserver/langserver/internal/utils"
	"github.com/sourcegraph/go-langserver/pkg/lsp"
	"github.com/sourcegraph/jsonrpc2"
)

var (
	GocodeCompletionEnabled = false
	CIKConstantSupported    = lsp.CIKVariable // or lsp.CIKConstant if client supported
	FuncSnippetEnabled      = false
	funcArgsRegexp          = regexp.MustCompile("func\\(([^)]+)\\)")
)

func (h *LangHandler) handleTextDocumentCompletion(ctx context.Context, conn jsonrpc2.JSONRPC2, req *jsonrpc2.Request, params lsp.CompletionParams) (*lsp.CompletionList, error) {
	if !utils.IsURI(params.TextDocument.URI) {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: fmt.Sprintf("textDocument/completion not yet supported for out-of-workspace URI (%q)", params.TextDocument.URI),
		}
	}

	// In the case of testing, our OS paths and VFS paths do not match. In the
	// real world, this is never the case. Give the test suite the opportunity
	// to correct the path now.
	vfsURI := params.TextDocument.URI
	if testOSToVFSPath != nil {
		vfsURI = utils.PathToURI(testOSToVFSPath(utils.UriToPath(vfsURI)))
	}

	// Read file contents and calculate byte offset.
	contents, err := h.readFile(ctx, vfsURI)
	if err != nil {
		return nil, err
	}
	// convert the path into a real path because 3rd party tools
	// might load additional code based on the file's package
	filename := utils.UriToRealPath(params.TextDocument.URI)
	offset, valid, why := offsetForPosition(contents, params.Position)
	if !valid {
		return nil, fmt.Errorf("invalid position: %s:%d:%d (%s)", filename, params.Position.Line, params.Position.Character, why)
	}

	ca, rangelen := gocode.AutoComplete(contents, filename, offset)
	citems := make([]lsp.CompletionItem, len(ca))
	for i, it := range ca {
		var kind lsp.CompletionItemKind
		switch it.Class.String() {
		case "const":
			kind = CIKConstantSupported
		case "func":
			kind = lsp.CIKFunction
		case "import":
			kind = lsp.CIKModule
		case "package":
			kind = lsp.CIKModule
		case "type":
			kind = lsp.CIKClass
		case "var":
			kind = lsp.CIKVariable
		}

		itf, newText := h.getNewText(kind, it.Name, it.Type)
		citems[i] = lsp.CompletionItem{
			Label:            it.Name,
			Kind:             kind,
			Detail:           it.Type,
			InsertTextFormat: itf,
			// InsertText is deprecated in favour of TextEdit, but added here for legacy client support
			InsertText: newText,
			TextEdit: &lsp.TextEdit{
				Range: lsp.Range{
					Start: lsp.Position{Line: params.Position.Line, Character: params.Position.Character - rangelen},
					End:   lsp.Position{Line: params.Position.Line, Character: params.Position.Character},
				},
				NewText: newText,
			},
		}
	}
	return &lsp.CompletionList{
		IsIncomplete: false,
		Items:        citems,
	}, nil
}

func (h *LangHandler) getNewText(kind lsp.CompletionItemKind, name, detail string) (lsp.InsertTextFormat, string) {
	if FuncSnippetEnabled &&
		kind == lsp.CIKFunction &&
		h.init.Capabilities.TextDocument.Completion.CompletionItem.SnippetSupport {
		args := genSnippetArgs(parseFuncArgs(detail))
		text := fmt.Sprintf("%s(%s)$0", name, strings.Join(args, ", "))
		return lsp.ITFSnippet, text
	}
	return lsp.ITFPlainText, name
}

func parseFuncArgs(def string) []string {
	m := funcArgsRegexp.FindStringSubmatch(def)
	var args []string
	if len(m) > 1 {
		args = strings.Split(m[1], ", ")
	}
	return args
}

func genSnippetArgs(args []string) []string {
	newArgs := make([]string, len(args))
	for i, a := range args {
		// Closing curly braces must be escaped
		a = strings.Replace(a, "}", "\\}", -1)
		newArgs[i] = fmt.Sprintf("${%d:%s}", i+1, a)
	}
	return newArgs
}
