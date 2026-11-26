package analysis

import (
	"context"
	"sort"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

type jinjaSymbol struct {
	name      string
	kindLabel string
	kind      SymbolKind
	nameStart int
	nameEnd   int
	fullStart int
	fullEnd   int
}

type openMacroState struct {
	idx       int
	fullStart int
}

func scanJinjaSymbols(src string, tokens []syntax.Token) []jinjaSymbol {
	var syms []jinjaSymbol
	var macroStack []openMacroState

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Kind != syntax.TokenJinjaBlockOpen {
			continue
		}

		endIdx := findJinjaBlockClose(tokens, i)
		syms, macroStack = processJinjaBlockTag(src, tokens, i, endIdx, syms, macroStack)
		i = endIdx
	}

	return syms
}

func processJinjaBlockTag(
	src string,
	tokens []syntax.Token,
	openIdx, endIdx int,
	syms []jinjaSymbol,
	macroStack []openMacroState,
) ([]jinjaSymbol, []openMacroState) {
	kwTok, kwIdx, hasKW := nextNonTriviaJinjaToken(tokens, openIdx+1, endIdx)
	if !hasKW || kwTok.Kind != syntax.TokenJinjaTagKeyword {
		return syms, macroStack
	}

	tagStart := tokens[openIdx].Start
	tagEnd := tokens[endIdx].End
	kw := kwTok.Text(src)

	if kw == "endmacro" {
		if len(macroStack) > 0 {
			top := macroStack[len(macroStack)-1]
			macroStack = macroStack[:len(macroStack)-1]
			syms[top.idx].fullEnd = tagEnd
		}

		return syms, macroStack
	}

	idTok, _, ok := nextNonTriviaJinjaToken(tokens, kwIdx+1, endIdx)
	if !ok || idTok.Kind != syntax.TokenJinjaIdent {
		return syms, macroStack
	}

	switch kw {
	case "macro":
		macroStack = append(macroStack, openMacroState{idx: len(syms), fullStart: tagStart})
		syms = append(syms, jinjaSymbol{
			name: idTok.Text(src), kindLabel: "macro", kind: SymbolKindFunction,
			nameStart: idTok.Start, nameEnd: idTok.End, fullStart: tagStart, fullEnd: tagEnd,
		})
	case "set", "for":
		syms = append(syms, jinjaSymbol{
			name: idTok.Text(src), kindLabel: kw, kind: SymbolKindVariable,
			nameStart: idTok.Start, nameEnd: idTok.End, fullStart: tagStart, fullEnd: tagEnd,
		})
	default:
	}

	return syms, macroStack
}

func findJinjaBlockClose(tokens []syntax.Token, openIdx int) int {
	for i := openIdx + 1; i < len(tokens); i++ {
		if tokens[i].Kind == syntax.TokenJinjaBlockClose {
			return i
		}
		if tokens[i].Mode != syntax.ModeJinjaTag {
			return i - 1
		}
	}

	return len(tokens) - 1
}

func nextNonTriviaJinjaToken(tokens []syntax.Token, from, limit int) (syntax.Token, int, bool) {
	for i := from; i <= limit && i < len(tokens); i++ {
		if !tokens[i].IsTrivia() {
			return tokens[i], i, true
		}
	}

	return syntax.Token{}, -1, false
}

// Definition returns go-to-definition locations for the symbol at pos in uri.
func (e *Engine) Definition(ctx context.Context, uri string, pos syntax.Position) ([]Location, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	origOff := snap.LineIndex.ByteOffset(pos)
	tok, ok := hoverTokenAt(snap.Tokens, origOff, len(snap.Text))
	if !ok {
		return []Location{}, nil
	}

	if tok.Mode == syntax.ModeJinjaExpr || tok.Mode == syntax.ModeJinjaTag {
		return definitionInJinja(uri, snap, tok), nil
	}

	if br, inBranch := findBranchAtOffset(snap.Branches, origOff); inBranch {
		entry.mu.Lock()
		defer entry.mu.Unlock()

		wasmMu.Lock()
		defer wasmMu.Unlock()

		return e.definitionInBranchWASM(uri, snap, br, tok, origOff), nil
	}

	shadowOff := snap.Projected.OffsetMap.OrigByteToShadow(origOff)
	st, hasStmt := snap.StatementAtByte(origOff)
	if !hasStmt {
		return []Location{}, nil
	}

	localOff := shadowOff - st.ShadowSpan.Start
	if localOff < 0 || localOff > len(st.SQL) {
		return []Location{}, nil
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	wasmMu.Lock()
	defer wasmMu.Unlock()

	return e.definitionInSQLWASM(uri, snap, st, tok, origOff, localOff), nil
}

func definitionInJinja(uri string, snap *DocumentSnapshot, tok syntax.Token) []Location {
	if tok.Kind != syntax.TokenJinjaIdent {
		return []Location{}
	}

	name := tok.Text(snap.Text)
	var matches []Location
	for _, sym := range scanJinjaSymbols(snap.Text, snap.Tokens) {
		if sym.name == name && sym.nameStart <= tok.Start {
			matches = append(matches, Location{
				URI:   uri,
				Range: snap.LineIndex.RangeAt(sym.nameStart, sym.nameEnd),
			})
		}
	}

	if len(matches) == 0 {
		for _, sym := range scanJinjaSymbols(snap.Text, snap.Tokens) {
			if sym.name == name {
				matches = append(matches, Location{
					URI:   uri,
					Range: snap.LineIndex.RangeAt(sym.nameStart, sym.nameEnd),
				})
			}
		}
	}

	if len(matches) > 0 {
		return []Location{matches[len(matches)-1]}
	}

	return []Location{}
}

func findBranchAtOffset(branches []BranchAnalysis, origOff int) (syntax.BranchSpan, bool) {
	var best syntax.BranchSpan
	found := false

	for _, ba := range branches {
		br := ba.Branch
		if origOff < br.BodyStart || origOff > br.BodyEnd {
			continue
		}

		if !found || br.Depth > best.Depth || (br.Depth == best.Depth && (br.BodyEnd-br.BodyStart) < (best.BodyEnd-best.BodyStart)) {
			best = br
			found = true
		}
	}

	return best, found
}

func (e *Engine) definitionInBranchWASM(
	uri string,
	snap *DocumentSnapshot,
	br syntax.BranchSpan,
	tok syntax.Token,
	origOff int,
) []Location {
	brProj := syntax.ProjectBranch(snap.Text, snap.Tokens, br)
	brSpans := syntax.SplitStatements(brProj.SQL, nil)
	shadowOff := brProj.OffsetMap.OrigByteToShadow(origOff)

	sp, ok := statementSpanForBranchOffset(brProj.SQL, brSpans, shadowOff)
	if !ok {
		return []Location{}
	}

	localOff := shadowOff - sp.Start
	if localOff < 0 || localOff > sp.End-sp.Start {
		return []Location{}
	}

	origStart, origEnd := shadowSpanToBestOrig(brProj.OffsetMap, sp.Start, sp.End)
	branchSnap := *snap
	branchSnap.Projected = brProj

	brSt := &AnalyzedStatement{
		ShadowSpan: syntax.ByteSpan{Start: sp.Start, End: sp.End},
		OrigSpan:   syntax.ByteSpan{Start: origStart, End: origEnd},
		SQL:        brProj.SQL[sp.Start:sp.End],
	}

	_, astStmt, parseErr := e.parseSingleStatementWASM(brSt.SQL)
	if parseErr == nil && !isNilNode(astStmt) {
		brSt.ASTStmt = astStmt
	} else {
		brSt.ParseErr = parseErr
	}

	return e.definitionInSQLWASM(uri, &branchSnap, brSt, tok, origOff, localOff)
}

func statementSpanForBranchOffset(sqlText string, spans []syntax.StatementSpan, shadowOff int) (syntax.StatementSpan, bool) {
	var boundaryMatch syntax.StatementSpan
	hasBoundary := false

	for _, sp := range spans {
		if strings.TrimSpace(sqlText[sp.Start:sp.End]) == "" {
			continue
		}

		if shadowOff >= sp.Start && shadowOff < sp.End {
			return sp, true
		}

		if shadowOff == sp.End {
			boundaryMatch = sp
			hasBoundary = true
		}
	}

	return boundaryMatch, hasBoundary
}

func (e *Engine) definitionInSQLWASM(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	tok syntax.Token,
	origOff int,
	localOff int,
) []Location {
	lookupOff := localOff
	if lookupOff > 0 && origOff == len(snap.Text) && isIdentOrBacktick(tok) && tok.End == origOff {
		lookupOff--
	}

	if !isNilNode(st.ASTStmt) {
		ctx := defASTContext{
			uri:     uri,
			snap:    snap,
			st:      st,
			astStmt: st.ASTStmt,
			unmap:   identitySpan,
		}
		if locs, handled := definitionFromAST(ctx, lookupOff); handled {
			if locs == nil {
				return []Location{}
			}

			return locs
		}

		return []Location{}
	}

	if isNilNode(st.ASTStmt) && isIdentOrBacktick(tok) {
		if locs, handled := e.definitionFromRepairedASTWASM(uri, snap, st, lookupOff); handled {
			if locs == nil {
				return []Location{}
			}

			return locs
		}

		if locs := definitionFromTokens(uri, snap, st, lookupOff); len(locs) > 0 {
			return locs
		}

		return []Location{}
	}

	word := strings.Trim(tok.Text(snap.Text), "`")
	if word != "" {
		if locs := lookupScriptSymbolDefinition(uri, snap, word, origOff); len(locs) > 0 {
			return locs
		}
	}

	return []Location{}
}

func definitionFromAST(ctx defASTContext, localOff int) ([]Location, bool) {
	idNode := findIdentifierAtOffset(ctx.astStmt, localOff)
	if isNilNode(idNode) {
		return nil, false
	}

	name, err := idNode.GetAsString()
	if err != nil || name == "" {
		return nil, false
	}

	if pe, isPE := astParent(idNode).(*googlesql.ASTPathExpression); isPE && !isNilNode(pe) {
		return definitionFromPathExpr(ctx, pe, idNode, name)
	}

	if isDirectNonScriptIdent(idNode) || isInsideFunctionDefBody(idNode) {
		return []Location{}, true
	}

	if locs := lookupScriptSymbolDefinition(ctx.uri, ctx.snap, name, 0); len(locs) > 0 {
		return locs, true
	}

	return []Location{}, true
}

func isDirectNonScriptIdent(idNode *googlesql.ASTIdentifier) bool {
	switch parent := astParent(idNode).(type) {
	case *googlesql.ASTDotIdentifier, *googlesql.ASTAlias, *googlesql.ASTColumnList,
		*googlesql.ASTStarExceptList, *googlesql.ASTPipeRenameItem, *googlesql.ASTUsingClause:
		return true
	case *googlesql.ASTColumnDefinition:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTStructColumnField:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTStructField:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTFunctionParameter:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTTVFSchemaColumn:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTWindowDefinition:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTWindowSpecification:
		return matchesChildIdent(parent, parent.BaseWindowName, idNode)
	case *googlesql.ASTRowPatternVariable:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTGraphElementPatternFiller:
		return matchesChildIdent(parent, parent.VariableName, idNode)
	case *googlesql.ASTGraphElementLabel:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTGraphElementLabelAndProperties:
		return matchesChildIdent(parent, parent.LabelName, idNode)
	case *googlesql.ASTGraphPathPattern:
		return matchesChildIdent(parent, parent.PathName, idNode)
	case *googlesql.ASTGraphPropertyNameAndValue:
		return matchesChildIdent(parent, parent.PropertyName, idNode)
	case *googlesql.ASTGraphNodeTableReference:
		return matchesChildIdent(parent, parent.NodeTableIdentifier, idNode)
	case *googlesql.ASTStarReplaceItem:
		return matchesChildIdent(parent, parent.Alias, idNode)
	case *googlesql.ASTPipeSetItem:
		return matchesChildIdent(parent, parent.Column, idNode)
	case *googlesql.ASTNamedArgument:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTOptionsEntry:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTHintEntry:
		return matchesChildIdent(parent, parent.Name, idNode) || matchesChildIdent(parent, parent.Qualifier, idNode)
	case *googlesql.ASTSampleClause:
		return matchesChildIdent(parent, parent.SampleMethod, idNode)
	case *googlesql.ASTColumnWithOptions:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTCreateProcedureStatement:
		return matchesChildIdent(parent, parent.Language, idNode)
	case googlesql.ASTCreateFunctionStmtBaseNode:
		return matchesChildIdent(parent, parent.Language, idNode)
	case *googlesql.ASTForInStatement:
		return matchesChildIdent(parent, parent.Variable, idNode)
	case *googlesql.ASTLabel:
		return matchesChildIdent(parent, parent.Name, idNode)
	case *googlesql.ASTAliasedQuery:
		return matchesChildIdent(parent, parent.Alias, idNode)
	case *googlesql.ASTUnpivotInItemLabel:
		if isNilNode(parent) {
			return false
		}
		lbl, _ := parent.Label()
		return isSameASTNode(lbl, idNode)
	case *googlesql.ASTIdentifierList:
		switch astParent(parent).(type) {
		case *googlesql.ASTAssignmentFromStruct, *googlesql.ASTExecuteIntoClause:
			// SET (x, y) = ... and EXECUTE IMMEDIATE ... INTO x, y resolve script variables.
			return false
		default:
			return true
		}
	default:
		return isDirectNonScriptDDLIdent(parent, idNode)
	}
}

func isDirectNonScriptDDLIdent(parent googlesql.ASTNode, idNode *googlesql.ASTIdentifier) bool {
	switch p := parent.(type) {
	case *googlesql.ASTCreateRowAccessPolicyStatement:
		return matchesChildIdent(p, p.Name, idNode)
	case *googlesql.ASTDropRowAccessPolicyStatement:
		return matchesChildIdent(p, p.Name, idNode)
	case *googlesql.ASTAlterRowAccessPolicyStatement:
		return matchesChildIdent(p, p.Name, idNode)
	case *googlesql.ASTAlterColumnTypeAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAlterColumnOptionsAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAlterColumnSetDefaultAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAlterColumnDropDefaultAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAlterColumnDropNotNullAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAlterColumnSetGeneratedAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAlterColumnDropGeneratedAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTDropColumnAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTAddColumnIdentifierAction:
		return matchesChildIdent(p, p.ColumnName, idNode)
	case *googlesql.ASTRenameColumnAction:
		return matchesChildIdent(p, p.ColumnName, idNode) || matchesChildIdent(p, p.NewColumnName, idNode)
	case *googlesql.ASTDropConstraintAction:
		return matchesChildIdent(p, p.ConstraintName, idNode)
	case *googlesql.ASTAlterConstraintEnforcementAction:
		return matchesChildIdent(p, p.ConstraintName, idNode)
	case *googlesql.ASTAlterConstraintSetOptionsAction:
		return matchesChildIdent(p, p.ConstraintName, idNode)
	case googlesql.ASTTableConstraintNode:
		return matchesChildIdent(p, p.ConstraintName, idNode)
	case *googlesql.ASTPrimaryKeyElement:
		return matchesChildIdent(p, p.Column, idNode)
	case *googlesql.ASTAddSubEntityAction:
		return matchesChildIdent(p, p.Name, idNode)
	case *googlesql.ASTAlterSubEntityAction:
		return matchesChildIdent(p, p.Name, idNode)
	case *googlesql.ASTDropSubEntityAction:
		return matchesChildIdent(p, p.Name, idNode)
	case *googlesql.ASTDescriptorColumn:
		return matchesChildIdent(p, p.Name, idNode)
	default:
		return false
	}
}

func matchesChildIdent(
	parent googlesql.ASTNode,
	childFn func() (*googlesql.ASTIdentifier, error),
	idNode *googlesql.ASTIdentifier,
) bool {
	if isNilNode(parent) {
		return false
	}

	child, _ := childFn()

	return isSameASTNode(child, idNode)
}

func isInsideFunctionDefBody(node googlesql.ASTNode) bool {
	for cur := node; !isNilNode(cur); cur = astParent(cur) {
		switch cur.(type) {
		case *googlesql.ASTCreateFunctionStatement,
			*googlesql.ASTCreateTableFunctionStatement,
			*googlesql.ASTCreateProcedureStatement:
			return true
		default:
		}
	}

	return false
}

func definitionFromPathExpr(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	idNode *googlesql.ASTIdentifier,
	name string,
) ([]Location, bool) {
	peParent := astParent(pe)
	if _, isTP := peParent.(*googlesql.ASTTablePathExpression); isTP || isDMLTargetPath(peParent, pe) {
		return definitionFromTablePathExpr(ctx, pe, name), true
	}

	if isNonScriptPathExpr(peParent, pe) {
		return []Location{}, true
	}

	isSetTarget := isUpdateSetTargetPath(pe)

	firstID, _ := pe.Name(0)
	num, _ := pe.NumNames()
	if num >= 2 {
		if isSameASTNode(firstID, idNode) {
			if isSetTarget {
				return definitionFromSetTargetQualifier(ctx, pe, name), true
			}

			return definitionFromQualifierSegment(ctx, pe, name), true
		}

		// Column/struct field segments (e.g. u.user_id) must not fall through to DECLARE/TEMP TABLE.
		return []Location{}, true
	}

	if num == 1 {
		if isSetTarget {
			return []Location{}, true
		}

		return definitionFromSingleIdentPath(ctx, pe, idNode, name), true
	}

	return []Location{}, true
}

func isNonScriptPathExpr(peParent googlesql.ASTNode, pe *googlesql.ASTPathExpression) bool {
	if isNilNode(peParent) || isNilNode(pe) {
		return false
	}

	switch p := peParent.(type) {
	case *googlesql.ASTFunctionCall:
		fnPath, _ := p.Function()
		return isSameASTNode(fnPath, pe) && !isRepairedDanglingDotPath(pe)
	case *googlesql.ASTTVF:
		tvfName, _ := p.Name()
		return isSameASTNode(tvfName, pe)
	case *googlesql.ASTGraphTableQuery:
		ref, _ := p.GraphReference()
		return isSameASTNode(ref, pe)
	case *googlesql.ASTGqlGraphPatternQuery:
		ref, _ := p.GraphReference()
		return isSameASTNode(ref, pe)
	case *googlesql.ASTUnpivotClause:
		col, _ := p.UnpivotOutputNameColumn()
		return isSameASTNode(col, pe)
	case *googlesql.ASTPathExpressionList:
		return isUnpivotPathList(p, pe)
	case *googlesql.ASTFunctionDeclaration:
		nm, _ := p.Name()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTCallStatement:
		nm, _ := p.ProcedureName()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTSimpleType:
		tn, _ := p.TypeName()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTSimpleColumnSchema:
		tn, _ := p.TypeName()
		return isSameASTNode(tn, pe)
	default:
		return isNonScriptDDLPathExpr(peParent, pe)
	}
}

func isNonScriptDDLPathExpr(peParent googlesql.ASTNode, pe *googlesql.ASTPathExpression) bool {
	if ddl, ok := peParent.(googlesql.ASTDdlStatementNode); ok && !isNilNode(ddl) {
		if tgt, _ := ddl.GetDdlTarget(); isSameASTNode(tgt, pe) {
			return true
		}
	}

	if ct, ok := peParent.(googlesql.ASTCreateTableStmtBaseNode); ok && !isNilNode(ct) {
		if like, _ := ct.LikeTableName(); isSameASTNode(like, pe) {
			return true
		}
	}

	if ds, ok := peParent.(googlesql.ASTTableDataSourceNode); ok && !isNilNode(ds) {
		if src, _ := ds.PathExpr(); isSameASTNode(src, pe) {
			return true
		}
	}

	if di, ok := peParent.(googlesql.ASTDropIndexStatementNode); ok && !isNilNode(di) {
		if tn, _ := di.TableName(); isSameASTNode(tn, pe) {
			return true
		}
	}

	return isNonScriptSecondaryDDLPathExpr(peParent, pe)
}

func isNonScriptSecondaryDDLPathExpr(peParent googlesql.ASTNode, pe *googlesql.ASTPathExpression) bool {
	switch p := peParent.(type) {
	case *googlesql.ASTConnectionClause:
		cp, _ := p.ConnectionPath()
		return isSameASTNode(cp, pe)
	case *googlesql.ASTExportModelStatement:
		mp, _ := p.ModelNamePath()
		return isSameASTNode(mp, pe)
	case *googlesql.ASTExportMetadataStatement:
		np, _ := p.NamePath()
		return isSameASTNode(np, pe)
	case *googlesql.ASTCloneDataStatement:
		tp, _ := p.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTCreateRowAccessPolicyStatement:
		tp, _ := p.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTDropRowAccessPolicyStatement:
		tn, _ := p.TableName()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTDropAllRowAccessPoliciesStatement:
		tn, _ := p.TableName()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTAlterAllRowAccessPoliciesStatement:
		tn, _ := p.TableNamePath()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTCreateIndexStatement:
		tn, _ := p.TableName()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTAlterIndexStatement:
		tn, _ := p.TableName()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTCreateMaterializedViewStatement:
		rs, _ := p.ReplicaSource()
		return isSameASTNode(rs, pe)
	case *googlesql.ASTGraphElementTable:
		nm, _ := p.Name()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTRenameStatement:
		on, _ := p.OldName()
		nn, _ := p.NewName()
		return isSameASTNode(on, pe) || isSameASTNode(nn, pe)
	case *googlesql.ASTRenameToClause:
		nn, _ := p.NewName()
		return isSameASTNode(nn, pe)
	case *googlesql.ASTCreateDatabaseStatement:
		nm, _ := p.Name()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTCreateLocalityGroupStatement:
		nm, _ := p.Name()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTDescribeStatement:
		nm, _ := p.Name()
		fn, _ := p.OptionalFromName()
		return isSameASTNode(nm, pe) || isSameASTNode(fn, pe)
	case *googlesql.ASTShowStatement:
		nm, _ := p.OptionalName()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTGrantStatement:
		tp, _ := p.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTRevokeStatement:
		tp, _ := p.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTImportStatement:
		nm, _ := p.Name()
		return isSameASTNode(nm, pe)
	case *googlesql.ASTModelClause:
		mp, _ := p.ModelPath()
		return isSameASTNode(mp, pe)
	case *googlesql.ASTForeignKeyReference:
		tn, _ := p.TableName()
		return isSameASTNode(tn, pe)
	case *googlesql.ASTTableAndColumnInfo:
		tn, _ := p.TableName()
		return isSameASTNode(tn, pe)
	default:
		return false
	}
}

func isUnpivotPathList(list *googlesql.ASTPathExpressionList, pe *googlesql.ASTPathExpression) bool {
	switch p := astParent(list).(type) {
	case *googlesql.ASTUnpivotClause:
		if isNilNode(p) {
			return false
		}
		outCols, _ := p.UnpivotOutputValueColumns()
		return isSameASTNode(outCols, list)
	case *googlesql.ASTUnpivotInItem:
		num, _ := pe.NumNames()
		return num == 1
	default:
		return false
	}
}

func definitionFromSingleIdentPath(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	idNode *googlesql.ASTIdentifier,
	name string,
) []Location {
	if locs, handled := resolvedColumnRefDef(ctx, pe, idNode, name); handled {
		return locs
	}

	if isPatternOrGraphVarRef(pe, name) {
		return []Location{}
	}

	if canResolveRangeVarAlias(ctx.st, pe, idNode) {
		if loc, ok := findEnclosingAliasDefinition(ctx, pe, name, true); ok {
			return []Location{loc}
		}
	}

	if loc, ok := findEnclosingWithOffsetDefinition(ctx, pe, name); ok {
		return []Location{loc}
	}

	if loc, ok := findEnclosingCTEDefinition(ctx, pe, name); ok {
		return []Location{loc}
	}

	rsList := resolvedStmtList(ctx.st)
	s, e, hasRange := astNodeByteRange(idNode)
	resolvedAsScriptSymbol := len(rsList) > 0 && hasRange && hasResolvedScriptSymbolAt(rsList, s, e)
	if !resolvedAsScriptSymbol && isSelectAliasRefInGroupOrOrderBy(ctx.snap, pe, name) {
		return []Location{}
	}

	if loc, ok := findEnclosingForInVarDefinition(ctx, pe, name); ok {
		return []Location{loc}
	}

	if isInsideFunctionDefBody(pe) {
		return []Location{}
	}

	if locs := lookupScriptSymbolDefinition(ctx.uri, ctx.snap, name, 0); len(locs) > 0 {
		return locs
	}

	return []Location{}
}

func isSelectAliasRefInGroupOrOrderBy(snap *DocumentSnapshot, pe *googlesql.ASTPathExpression, name string) bool {
	inGroupOrOrder := false
	crossedSubquery := false
	for cur := astParent(pe); !isNilNode(cur); cur = astParent(cur) {
		next, matched, done := stepSelectAliasRefAncestor(snap, pe, cur, name, &inGroupOrOrder, &crossedSubquery)
		if done {
			return matched
		}
		if !isNilNode(next) {
			cur = next
		}
	}

	return false
}

func stepSelectAliasRefAncestor(
	snap *DocumentSnapshot,
	pe *googlesql.ASTPathExpression,
	cur googlesql.ASTNode,
	name string,
	inGroupOrOrder, crossedSubquery *bool,
) (next googlesql.ASTNode, matched, done bool) {
	switch node := cur.(type) {
	case *googlesql.ASTGroupingItem, *googlesql.ASTOrderingExpression:
		*inGroupOrOrder = true
	case *googlesql.ASTGroupBy:
		matched, done = groupByAncestorAliasRef(snap, node, name, *inGroupOrOrder, *crossedSubquery)
		return nil, matched, done
	case *googlesql.ASTHaving:
		matched, done = selectClauseAncestorAliasRef(snap, selectForHaving(node), node, name)
		return nil, matched, done
	case *googlesql.ASTQualify:
		matched, done = selectClauseAncestorAliasRef(snap, selectForQualify(node), node, name)
		return nil, matched, done
	case *googlesql.ASTWhereClause:
		matched, done = whereClauseAliasRef(snap, node, name)
		return nil, matched, done
	case *googlesql.ASTOrderBy:
		matched, done = orderByAncestorAliasRef(snap, node, name, *inGroupOrOrder)
		return nil, matched, done
	case *googlesql.ASTSelect:
		matched, done = selectAncestorAliasRef(snap, node, name)
		return nil, matched, done
	case *googlesql.ASTFromQuery:
		matched, done = fromQueryAncestorAliasRef(snap, node, name)
		return nil, matched, done
	case *googlesql.ASTQuery, *googlesql.ASTSetOperation:
		return nil, false, queryOrSetOpAncestorAliasRef(node, crossedSubquery)
	case *googlesql.ASTExpressionSubquery:
		*crossedSubquery = true
	case googlesql.ASTPipeOperatorNode:
		matched, done = pipeOpAncestorAliasRef(snap, pe, node, name)
		return nil, matched, done
	case *googlesql.ASTWindowDefinition:
		if windowDefAliasRef(snap, node, name) {
			return nil, true, true
		}
		return nil, false, !hasEnclosingExprSubquery(node)
	case *googlesql.ASTTableSubquery:
		if exprParent := enclosingExprSubqueryParent(node); !isNilNode(exprParent) {
			*crossedSubquery = true
			return exprParent, false, false
		}
		return nil, false, true
	case *googlesql.ASTWithClauseEntry:
		return nil, false, true
	default:
	}

	return nil, false, false
}

func groupByAncestorAliasRef(
	snap *DocumentSnapshot,
	groupBy *googlesql.ASTGroupBy,
	name string,
	inGroupOrOrder, crossedSubquery bool,
) (matched, done bool) {
	if groupByAliasRef(snap, groupBy, name, inGroupOrOrder, crossedSubquery) {
		return true, true
	}

	return false, !groupByInExprSubquery(groupBy)
}

func selectClauseAncestorAliasRef(
	snap *DocumentSnapshot,
	sel *googlesql.ASTSelect,
	clause googlesql.ASTNode,
	name string,
) (matched, done bool) {
	if selectHasAliasOrTableCol(snap, sel, name) {
		return true, true
	}
	if !isNilNode(sel) {
		return false, !isExprSubquerySelect(sel) && !hasEnclosingExprSubquery(sel)
	}

	return false, !hasEnclosingExprSubquery(clause)
}

func pipeOpAncestorAliasRef(
	snap *DocumentSnapshot,
	pe *googlesql.ASTPathExpression,
	op googlesql.ASTPipeOperatorNode,
	name string,
) (matched, done bool) {
	if isExprBearingPipeOp(op) && (pipeOpHasPredecessorOutputAlias(snap, op, name) || pipeJoinHasRHSColumn(snap, pe, op, name)) {
		return true, true
	}

	return false, !hasEnclosingExprSubquery(op)
}

func pipeJoinHasRHSColumn(
	snap *DocumentSnapshot,
	pe *googlesql.ASTPathExpression,
	op googlesql.ASTPipeOperatorNode,
	name string,
) bool {
	pj, ok := op.(*googlesql.ASTPipeJoin)
	if !ok || isNilNode(pj) {
		return false
	}

	j, err := pj.Join()
	if err != nil || isNilNode(j) {
		return false
	}

	enclosingJoin := findEnclosingJoinForOnNode(j, pe)
	if isNilNode(enclosingJoin) {
		return false
	}

	return tableExprHasColumn(snap, pj, enclosingJoin, name)
}

func findEnclosingJoinForOnNode(te, target googlesql.ASTNode) *googlesql.ASTJoin {
	if isNilNode(te) || isNilNode(target) {
		return nil
	}

	switch node := te.(type) {
	case *googlesql.ASTJoin:
		if isNodeInJoinOn(node, target) {
			return node
		}
		lhs, _ := node.Lhs()
		if found := findEnclosingJoinForOnNode(lhs, target); !isNilNode(found) {
			return found
		}
		rhs, _ := node.Rhs()
		return findEnclosingJoinForOnNode(rhs, target)
	case *googlesql.ASTParenthesizedJoin:
		inner, _ := node.Join()
		return findEnclosingJoinForOnNode(inner, target)
	default:
		return nil
	}
}

func isNodeInJoinOn(j *googlesql.ASTJoin, node googlesql.ASTNode) bool {
	if isNilNode(j) || isNilNode(node) {
		return false
	}
	if on, err := j.OnClause(); err == nil && !isNilNode(on) && astNodeContains(on, node) {
		return true
	}

	return false
}

func groupByInExprSubquery(groupBy *googlesql.ASTGroupBy) bool {
	if pipeAgg := pipeAggregateForGroupBy(groupBy); !isNilNode(pipeAgg) {
		return hasEnclosingExprSubquery(pipeAgg)
	}
	if sel := selectForGroupBy(groupBy); !isNilNode(sel) {
		return isExprSubquerySelect(sel) || hasEnclosingExprSubquery(sel)
	}

	return hasEnclosingExprSubquery(groupBy)
}

func orderByAncestorAliasRef(
	snap *DocumentSnapshot,
	orderBy *googlesql.ASTOrderBy,
	name string,
	inGroupOrOrder bool,
) (matched, done bool) {
	if !isQueryOrPipeOrderBy(orderBy) {
		return false, false
	}
	if inGroupOrOrder && orderByHasSelectAlias(snap, orderBy, name) {
		return true, true
	}
	if !isExprSubqueryOrderBy(orderBy) && !isNestedTableSubqueryOrderBy(orderBy) {
		return false, true
	}

	return false, false
}

func queryOrSetOpAncestorAliasRef(node googlesql.ASTNode, crossedSubquery *bool) bool {
	if exprSubqueryAncestor(node) {
		*crossedSubquery = true
		return false
	}
	parent, _ := unwrapQueryAndSetOpParentWithChild(node)
	switch parent.(type) {
	case *googlesql.ASTTableSubquery, *googlesql.ASTPipeSetOperation:
		if hasEnclosingExprSubquery(node) {
			return false
		}
	default:
	}

	return true
}

func windowDefAliasRef(snap *DocumentSnapshot, wd *googlesql.ASTWindowDefinition, name string) bool {
	if sel := windowDefVisibleInQualifySelect(wd); !isNilNode(sel) {
		return selectHasAliasOrTableCol(snap, sel, name)
	}
	if sel := windowDefParentSelect(wd); !isNilNode(sel) {
		fc, _ := sel.FromClause()
		return fromClauseHasColumn(snap, sel, fc, name)
	}

	return false
}

func groupByAliasRef(snap *DocumentSnapshot, groupBy *googlesql.ASTGroupBy, name string, inGroupOrOrder, crossedSubquery bool) bool {
	if !inGroupOrOrder {
		return false
	}
	if pipeAgg := pipeAggregateForGroupBy(groupBy); !isNilNode(pipeAgg) {
		return pipeOpHasPredecessorOutputAlias(snap, pipeAgg, name)
	}

	sel := selectForGroupBy(groupBy)
	if crossedSubquery {
		if isNilNode(sel) {
			return false
		}
		fc, _ := sel.FromClause()
		return fromClauseHasColumn(snap, sel, fc, name)
	}

	return selectHasAliasOrTableCol(snap, sel, name)
}

func isQueryOrPipeOrderBy(orderBy *googlesql.ASTOrderBy) bool {
	switch astParent(orderBy).(type) {
	case *googlesql.ASTQuery, *googlesql.ASTPipeOrderBy:
		return true
	default:
		return false
	}
}

func isExprSubqueryOrderBy(orderBy *googlesql.ASTOrderBy) bool {
	switch parent := astParent(orderBy).(type) {
	case *googlesql.ASTQuery:
		return !isNilNode(parent) && exprSubqueryAncestor(parent)
	case *googlesql.ASTPipeOrderBy:
		return !isNilNode(parent) && hasEnclosingExprSubquery(parent)
	default:
		return false
	}
}

func isNestedTableSubqueryOrderBy(orderBy *googlesql.ASTOrderBy) bool {
	q, ok := astParent(orderBy).(*googlesql.ASTQuery)
	if !ok || isNilNode(q) {
		return false
	}

	parent, _ := unwrapQueryAndSetOpParentWithChild(q)
	switch parent.(type) {
	case *googlesql.ASTTableSubquery, *googlesql.ASTPipeSetOperation:
		return hasEnclosingExprSubquery(q)
	default:
		return false
	}
}

func windowDefParentSelect(wd *googlesql.ASTWindowDefinition) *googlesql.ASTSelect {
	if isNilNode(wd) {
		return nil
	}

	wc, ok := astParent(wd).(*googlesql.ASTWindowClause)
	if !ok || isNilNode(wc) {
		return nil
	}

	sel, ok := astParent(wc).(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	return sel
}

func windowDefVisibleInQualifySelect(wd *googlesql.ASTWindowDefinition) *googlesql.ASTSelect {
	sel := windowDefParentSelect(wd)
	if isNilNode(sel) {
		return nil
	}

	winNameNode, _ := wd.Name()
	if isNilNode(winNameNode) {
		return nil
	}

	winName, err := winNameNode.GetAsString()
	if err != nil || winName == "" {
		return nil
	}

	qual, _ := sel.Qualify()
	if isNilNode(qual) {
		return nil
	}

	sl, _ := sel.SelectList()
	if subtreeRefsWindowName(sl, winName) || !subtreeRefsWindowName(qual, winName) {
		return nil
	}

	return sel
}

func subtreeRefsWindowName(root googlesql.ASTNode, winName string) bool {
	if isNilNode(root) {
		return false
	}

	found := false
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if found {
			return false
		}
		ws, ok := n.(*googlesql.ASTWindowSpecification)
		if !ok || isNilNode(ws) {
			return true
		}
		base, _ := ws.BaseWindowName()
		if matchesIdentName(base, winName) {
			found = true
			return false
		}
		return true
	})

	return found
}

func selectHasAliasOrTableCol(snap *DocumentSnapshot, sel *googlesql.ASTSelect, name string) bool {
	if selectListHasAlias(snap, sel, name) {
		return true
	}
	if isNilNode(sel) {
		return false
	}

	fc, _ := sel.FromClause()

	return fromClauseHasColumn(snap, sel, fc, name)
}

func selectAncestorAliasRef(snap *DocumentSnapshot, sel *googlesql.ASTSelect, name string) (matched, done bool) {
	if pipeOp, isPipe := astParent(sel).(googlesql.ASTPipeOperatorNode); isPipe && isExprBearingPipeOp(pipeOp) {
		if pipeOpHasPredecessorOutputAlias(snap, pipeOp, name) {
			return true, true
		}

		return false, !hasEnclosingExprSubquery(pipeOp)
	}
	if !isNilNode(sel) {
		if fc, err := sel.FromClause(); err == nil && !isNilNode(fc) && fromClauseHasColumn(snap, sel, fc, name) {
			return true, true
		}
	}
	if !isExprSubquerySelect(sel) && !hasEnclosingExprSubquery(sel) {
		return false, true
	}

	return false, false
}

func fromQueryAncestorAliasRef(snap *DocumentSnapshot, fq *googlesql.ASTFromQuery, name string) (matched, done bool) {
	if !isNilNode(fq) {
		if fc, err := fq.FromClause(); err == nil && !isNilNode(fc) && fromClauseHasColumn(snap, fq, fc, name) {
			return true, true
		}
	}

	return false, !hasEnclosingExprSubquery(fq)
}

func pipeAggregateForGroupBy(groupBy *googlesql.ASTGroupBy) *googlesql.ASTPipeAggregate {
	sel, ok := astParent(groupBy).(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	pipeAgg, ok := astParent(sel).(*googlesql.ASTPipeAggregate)
	if !ok || isNilNode(pipeAgg) {
		return nil
	}

	if gb, _ := sel.GroupBy(); isSameASTNode(gb, groupBy) {
		return pipeAgg
	}

	return nil
}

func isExprSubquerySelect(sel *googlesql.ASTSelect) bool {
	if isNilNode(sel) {
		return false
	}

	return exprSubqueryAncestor(sel)
}

func unwrapQueryAndSetOpParentWithChild(node googlesql.ASTNode) (parent, child googlesql.ASTNode) {
	child = node
	cur := astParent(node)
	for !isNilNode(cur) {
		switch cur.(type) {
		case *googlesql.ASTQuery, *googlesql.ASTSetOperation:
			child = cur
			cur = astParent(cur)
		default:
			return cur, child
		}
	}

	return nil, child
}

func exprSubqueryAncestor(node googlesql.ASTNode) bool {
	parent, child := unwrapQueryAndSetOpParentWithChild(node)
	return isExprSubqueryParentEdge(parent, child)
}

func hasEnclosingExprSubquery(node googlesql.ASTNode) bool {
	return !isNilNode(enclosingExprSubqueryParent(node))
}

func enclosingExprSubqueryParent(node googlesql.ASTNode) googlesql.ASTNode {
	prev := node
	for cur := astParent(node); !isNilNode(cur); cur = astParent(cur) {
		if isExprSubqueryParentEdge(cur, prev) {
			return cur
		}
		switch cur.(type) {
		case *googlesql.ASTWithClauseEntry,
			*googlesql.ASTWithClause,
			googlesql.ASTStatementNode,
			*googlesql.ASTScript:
			return nil
		default:
		}
		prev = cur
	}

	return nil
}

func isExprSubqueryParentEdge(parent, child googlesql.ASTNode) bool {
	if isNilNode(parent) || isNilNode(child) {
		return false
	}

	switch p := parent.(type) {
	case *googlesql.ASTExpressionSubquery:
		q, _ := p.Query()
		return isSameASTNode(q, child)
	case *googlesql.ASTInExpression:
		q, _ := p.Query()
		return isSameASTNode(q, child)
	case *googlesql.ASTQuantifiedComparisonExpression:
		q, _ := p.Query()
		return isSameASTNode(q, child)
	case *googlesql.ASTLikeExpression:
		q, _ := p.Query()
		return isSameASTNode(q, child)
	default:
		return false
	}
}

func isExprBearingPipeOp(op googlesql.ASTPipeOperatorNode) bool {
	if isNilNode(op) {
		return false
	}

	switch op.(type) {
	case *googlesql.ASTPipeWhere, *googlesql.ASTPipeExtend, *googlesql.ASTPipeSelect,
		*googlesql.ASTPipeAggregate, *googlesql.ASTPipeWindow, *googlesql.ASTPipeSet,
		*googlesql.ASTPipeAssert, *googlesql.ASTPipeJoin, *googlesql.ASTPipeIfCase,
		*googlesql.ASTPipeIf, *googlesql.ASTPipePivot, *googlesql.ASTPipeUnpivot:
		return true
	default:
		return false
	}
}

func selectForGroupBy(groupBy *googlesql.ASTGroupBy) *googlesql.ASTSelect {
	sel, ok := astParent(groupBy).(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	if _, isPipeAgg := astParent(sel).(*googlesql.ASTPipeAggregate); isPipeAgg {
		return nil
	}

	if gb, _ := sel.GroupBy(); isSameASTNode(gb, groupBy) {
		return sel
	}

	return nil
}

func selectForHaving(having *googlesql.ASTHaving) *googlesql.ASTSelect {
	sel, ok := astParent(having).(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	if hv, _ := sel.Having(); isSameASTNode(hv, having) {
		return sel
	}

	return nil
}

func selectForQualify(qual *googlesql.ASTQualify) *googlesql.ASTSelect {
	sel, ok := astParent(qual).(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	if qf, _ := sel.Qualify(); isSameASTNode(qf, qual) {
		return sel
	}

	return nil
}

func whereClauseAliasRef(snap *DocumentSnapshot, where *googlesql.ASTWhereClause, name string) (matched, done bool) {
	sel := selectForWhere(where)
	if isNilNode(sel) {
		return false, false
	}

	fc, _ := sel.FromClause()
	if fromClauseHasColumn(snap, sel, fc, name) {
		return true, true
	}

	if !isExprSubquerySelect(sel) && !hasEnclosingExprSubquery(sel) {
		return false, true
	}

	return false, false
}

func selectForWhere(where *googlesql.ASTWhereClause) *googlesql.ASTSelect {
	if isNilNode(where) {
		return nil
	}

	sel, ok := astParent(where).(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	if wc, _ := sel.WhereClause(); isSameASTNode(wc, where) {
		return sel
	}

	return nil
}

func orderByHasSelectAlias(snap *DocumentSnapshot, orderBy *googlesql.ASTOrderBy, name string) bool {
	switch parent := astParent(orderBy).(type) {
	case *googlesql.ASTQuery:
		if isNilNode(parent) {
			return false
		}
		if ob, _ := parent.OrderBy(); isSameASTNode(ob, orderBy) {
			return queryHasOrderByScopeCol(snap, parent, name)
		}
	case *googlesql.ASTPipeOrderBy:
		if isNilNode(parent) {
			return false
		}
		if ob, _ := parent.OrderBy(); isSameASTNode(ob, orderBy) {
			return pipeOrderByHasOutputAlias(snap, parent, name)
		}
	default:
	}

	return false
}

func queryHasOrderByScopeCol(snap *DocumentSnapshot, q *googlesql.ASTQuery, name string) bool {
	if isNilNode(q) {
		return false
	}

	if matched, stopped := pipeParentHasOutputAlias(snap, q, astNumChildren(q)-1, name); stopped {
		return matched
	}

	qe, _ := q.QueryExpr()
	if sel, ok := qe.(*googlesql.ASTSelect); ok && !isNilNode(sel) {
		return selectHasAliasOrTableCol(snap, sel, name)
	}

	return queryExprHasOutputAlias(snap, qe, name)
}

func queryHasOutputAlias(snap *DocumentSnapshot, q *googlesql.ASTQuery, name string) bool {
	if isNilNode(q) {
		return false
	}

	if matched, stopped := pipeParentHasOutputAlias(snap, q, astNumChildren(q)-1, name); stopped {
		return matched
	}

	qe, _ := q.QueryExpr()

	return queryExprHasOutputAlias(snap, qe, name)
}

func queryExprHasOutputAlias(snap *DocumentSnapshot, qe googlesql.ASTQueryExpressionNode, name string) bool {
	for !isNilNode(qe) {
		switch node := qe.(type) {
		case *googlesql.ASTSelect:
			return selectHasAliasOrProjectedCol(snap, node, name)
		case *googlesql.ASTQuery:
			return queryHasOutputAlias(snap, node, name)
		case *googlesql.ASTSetOperation:
			inp, _ := node.Inputs(0)
			qe = inp
		case *googlesql.ASTFromQuery:
			fc, _ := node.FromClause()
			return fromClauseHasColumn(snap, node, fc, name)
		default:
			return false
		}
	}

	return false
}

func selectHasAliasOrProjectedCol(snap *DocumentSnapshot, sel *googlesql.ASTSelect, name string) bool {
	if selectListHasAlias(snap, sel, name) {
		return true
	}
	if isNilNode(sel) {
		return false
	}

	fc, _ := sel.FromClause()
	if isNilNode(fc) {
		return false
	}

	return selectListProjectsColumnFromClause(snap, sel, fc, name)
}

func selectListProjectsColumnFromClause(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	fc *googlesql.ASTFromClause,
	name string,
) bool {
	sel, _ := scopeNode.(*googlesql.ASTSelect)
	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return false
	}

	n := astNumChildren(sl)
	for i := range n {
		col, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(col) || !isNilNode(matchASTAliasIdent(col.Alias())) {
			continue
		}
		expr, _ := col.Expression()
		if selectExprProjectsFromClause(snap, scopeNode, fc, expr, name) {
			return true
		}
	}

	return false
}

func selectExprProjectsFromClause(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	fc *googlesql.ASTFromClause,
	expr googlesql.ASTNode,
	name string,
) bool {
	if isUnqualifiedStarAllowing(expr, name) || isSingleIdentPathWithName(expr, name) {
		return fromClauseHasColumn(snap, scopeNode, fc, name)
	}

	if qualParts := dotStarQualifierParts(expr); len(qualParts) > 0 && starModifiersAllowPassThrough(starModifiersOfExpr(expr), name) {
		return fromClauseHasQualPathColumn(snap, scopeNode, fc, qualParts, name)
	}

	return false
}

func fromClauseHasColumn(snap *DocumentSnapshot, scopeNode googlesql.ASTNode, fc *googlesql.ASTFromClause, name string) bool {
	if isNilNode(fc) {
		return false
	}

	te, _ := fc.TableExpression()

	return tableExprHasColumn(snap, scopeNode, te, name)
}

func tableExprHasColumn(snap *DocumentSnapshot, scopeNode, te googlesql.ASTNode, name string) bool {
	if isNilNode(te) {
		return false
	}

	switch node := te.(type) {
	case *googlesql.ASTTableSubquery:
		sub, _ := node.Subquery()
		return queryHasOutputAlias(snap, sub, name)
	case *googlesql.ASTTablePathExpression:
		return tablePathHasColumn(snap, scopeNode, node, name)
	case *googlesql.ASTJoin:
		lhs, _ := node.Lhs()
		rhs, _ := node.Rhs()
		return tableExprHasColumn(snap, scopeNode, lhs, name) || tableExprHasColumn(snap, scopeNode, rhs, name)
	case *googlesql.ASTParenthesizedJoin:
		j, _ := node.Join()
		return tableExprHasColumn(snap, scopeNode, j, name)
	default:
		return false
	}
}

func tablePathHasColumn(snap *DocumentSnapshot, scopeNode googlesql.ASTNode, tp *googlesql.ASTTablePathExpression, name string) bool {
	if isNilNode(tp) {
		return false
	}

	if cteQuery := resolveEnclosingCTEQuery(scopeNode, tp); !isNilNode(cteQuery) {
		return queryHasOutputAlias(snap, cteQuery, name)
	}

	if snap != nil {
		if meta := tableMetaFromTablePath(snap, tp); meta != nil {
			return tableMetaHasColumn(meta, name)
		}
	}

	return false
}

func fromClauseHasQualPathColumn(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	fc *googlesql.ASTFromClause,
	qualParts []string,
	name string,
) bool {
	if isNilNode(fc) || len(qualParts) == 0 {
		return false
	}

	te, _ := fc.TableExpression()

	return tableExprHasQualPathColumn(snap, scopeNode, te, qualParts, name)
}

func tableExprHasQualPathColumn(
	snap *DocumentSnapshot,
	scopeNode, te googlesql.ASTNode,
	qualParts []string,
	name string,
) bool {
	if isNilNode(te) || len(qualParts) == 0 {
		return false
	}

	switch node := te.(type) {
	case *googlesql.ASTTableSubquery:
		sub, _ := node.Subquery()
		if id := matchASTAliasIdent(node.Alias()); !isNilNode(id) && matchesIdentName(id, qualParts[0]) {
			if len(qualParts) == 1 {
				return queryHasOutputAlias(snap, sub, name)
			}
			return queryHasOutputAlias(snap, sub, qualParts[1]) && queryHasQualPathColumn(snap, sub, qualParts[1:], name)
		}
		return queryHasOutputAlias(snap, sub, qualParts[0]) && queryHasQualPathColumn(snap, sub, qualParts, name)
	case *googlesql.ASTTablePathExpression:
		return tablePathHasQualPathColumn(snap, scopeNode, node, qualParts, name)
	case *googlesql.ASTJoin:
		lhs, _ := node.Lhs()
		rhs, _ := node.Rhs()
		return tableExprHasQualPathColumn(snap, scopeNode, lhs, qualParts, name) ||
			tableExprHasQualPathColumn(snap, scopeNode, rhs, qualParts, name)
	case *googlesql.ASTParenthesizedJoin:
		j, _ := node.Join()
		return tableExprHasQualPathColumn(snap, scopeNode, j, qualParts, name)
	default:
	}

	return false
}

func tablePathHasQualPathColumn(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	tp *googlesql.ASTTablePathExpression,
	qualParts []string,
	name string,
) bool {
	if isNilNode(tp) || len(qualParts) == 0 {
		return false
	}

	if cteQuery := resolveEnclosingCTEQuery(scopeNode, tp); !isNilNode(cteQuery) {
		if len(qualParts) == 1 && tablePathMatchesAlias(tp, qualParts[0]) {
			return queryHasOutputAlias(snap, cteQuery, name)
		}
		if len(qualParts) >= 2 && tablePathMatchesAlias(tp, qualParts[0]) {
			return queryHasOutputAlias(snap, cteQuery, qualParts[1]) && queryHasQualPathColumn(snap, cteQuery, qualParts[1:], name)
		}
		return queryHasOutputAlias(snap, cteQuery, qualParts[0]) && queryHasQualPathColumn(snap, cteQuery, qualParts, name)
	}

	var meta *catalog.TableMeta
	if snap != nil {
		meta = tableMetaFromTablePath(snap, tp)
	}
	if meta == nil {
		return false
	}

	if tablePathMatchesAlias(tp, qualParts[0]) {
		if len(qualParts) == 1 {
			return tableMetaHasColumn(meta, name)
		}
		return tableMetaHasStructFieldPath(meta, qualParts[1:], name)
	}

	return tableMetaHasStructFieldPath(meta, qualParts, name)
}

func resolveEnclosingCTEQuery(scopeNode googlesql.ASTNode, tp *googlesql.ASTTablePathExpression) *googlesql.ASTQuery {
	pe, _ := tp.PathExpr()
	if isNilNode(pe) {
		return nil
	}

	num, _ := pe.NumNames()
	if num != 1 {
		return nil
	}

	last, _ := pe.LastName()
	if isNilNode(last) {
		return nil
	}

	cteName, err := last.GetAsString()
	if err != nil || cteName == "" {
		return nil
	}

	for cur := scopeNode; !isNilNode(cur); cur = astParent(cur) {
		q, ok := cur.(*googlesql.ASTQuery)
		if !ok || isNilNode(q) {
			continue
		}
		wc, _ := q.WithClause()
		if cteQ := matchCTEQueryInWithClause(wc, scopeNode, cteName); !isNilNode(cteQ) {
			return cteQ
		}
	}

	return nil
}

func matchCTEQueryInWithClause(wc *googlesql.ASTWithClause, scopeNode googlesql.ASTNode, cteName string) *googlesql.ASTQuery {
	if isNilNode(wc) {
		return nil
	}

	limit := astNumChildren(wc)
	if entryIdx := withClauseEntryIndexContaining(wc, scopeNode); entryIdx >= 0 {
		if rec, _ := wc.Recursive(); !rec {
			limit = entryIdx
		} else {
			// Skip self-referencing non-union recursive queries to avoid cycles.
			limit = entryIdx + 1
		}
	}

	for i := range limit {
		entry, ok := astChild(wc, i).(*googlesql.ASTWithClauseEntry)
		if !ok || isNilNode(entry) {
			continue
		}
		aq, _ := entry.AliasedQuery()
		if isNilNode(aq) {
			continue
		}
		al, _ := aq.Alias()
		if matchesIdentName(al, cteName) {
			q, _ := aq.Query()
			if isNodeWithinSubtree(scopeNode)(q, nil) && !queryHasNonSelfBranch(q, scopeNode) {
				return nil
			}
			return q
		}
	}

	return nil
}

func withClauseEntryIndexContaining(wc *googlesql.ASTWithClause, target googlesql.ASTNode) int {
	if !isNodeWithinSubtree(target)(wc, nil) {
		return -1
	}
	n := astNumChildren(wc)
	for i := range n {
		if isNodeWithinSubtree(target)(astChild(wc, i), nil) {
			return i
		}
	}
	return -1
}

func queryHasNonSelfBranch(q *googlesql.ASTQuery, scopeNode googlesql.ASTNode) bool {
	if isNilNode(q) {
		return false
	}
	qe, _ := q.QueryExpr()
	setOp, ok := qe.(*googlesql.ASTSetOperation)
	if !ok || isNilNode(setOp) {
		return false
	}
	inp0, _ := setOp.Inputs(0)
	return !isNodeWithinSubtree(scopeNode)(inp0, nil)
}

func tableMetaHasColumn(meta *catalog.TableMeta, name string) bool {
	if meta == nil || name == "" {
		return false
	}

	for i := range meta.Schema {
		if strings.EqualFold(meta.Schema[i].Name, name) {
			return true
		}
	}

	if meta.IsIngestionTimePartitioned() &&
		(strings.EqualFold(name, "_PARTITIONTIME") || strings.EqualFold(name, "_PARTITIONDATE")) {
		return true
	}

	return strings.HasSuffix(meta.Ref.Table, "*") && strings.EqualFold(name, "_TABLE_SUFFIX")
}

func tableMetaHasStructFieldPath(meta *catalog.TableMeta, structPath []string, fieldName string) bool {
	if meta == nil || len(structPath) == 0 || fieldName == "" {
		return false
	}

	col, ok := meta.FindField(structPath[0])
	if !ok {
		return false
	}

	fields := col.Fields
	for _, seg := range structPath[1:] {
		next, found := findNestedStructField(fields, seg)
		if !found {
			return false
		}
		fields = next.Fields
	}

	_, found := findNestedStructField(fields, fieldName)

	return found
}

func findNestedStructField(fields []catalog.ColumnMeta, name string) (catalog.ColumnMeta, bool) {
	for i := range fields {
		if strings.EqualFold(fields[i].Name, name) {
			return fields[i], true
		}
	}

	return catalog.ColumnMeta{}, false
}

func pipeOrderByHasOutputAlias(snap *DocumentSnapshot, pipeOrderBy *googlesql.ASTPipeOrderBy, name string) bool {
	return pipeOpHasPredecessorOutputAlias(snap, pipeOrderBy, name)
}

func pipeOpHasPredecessorOutputAlias(snap *DocumentSnapshot, op googlesql.ASTNode, name string) bool {
	if ifCase, ok := op.(*googlesql.ASTPipeIfCase); ok && !isNilNode(ifCase) {
		op = astParent(ifCase)
	}

	parent := astParent(op)
	if isNilNode(parent) {
		return false
	}

	idx := childIndexInParent(parent, op)
	if idx < 0 {
		return false
	}

	if matched, stopped := pipeParentHasOutputAlias(snap, parent, idx-1, name); stopped {
		return matched
	}

	switch p := parent.(type) {
	case *googlesql.ASTQuery:
		qe, _ := p.QueryExpr()
		return queryExprHasOutputAlias(snap, qe, name)
	case *googlesql.ASTSubpipeline:
		return subpipelineBaseHasOutputAlias(snap, p, name)
	default:
		return false
	}
}

func childIndexInParent(parent, target googlesql.ASTNode) int {
	n := astNumChildren(parent)
	for i := range n {
		if isSameASTNode(astChild(parent, i), target) {
			return i
		}
	}

	return -1
}

func subpipelineBaseHasOutputAlias(snap *DocumentSnapshot, sub *googlesql.ASTSubpipeline, name string) bool {
	switch p := astParent(sub).(type) {
	case *googlesql.ASTSubpipelineStatement:
		if isNilNode(p) {
			return false
		}
		swp, ok := astParent(p).(*googlesql.ASTStatementWithPipeOperators)
		if !ok || isNilNode(swp) {
			return false
		}
		stmt, _ := swp.Statement()
		if qs, ok := stmt.(*googlesql.ASTQueryStatement); ok && !isNilNode(qs) {
			q, _ := qs.Query()
			return queryHasOutputAlias(snap, q, name)
		}
		return false
	case *googlesql.ASTPipeTee, *googlesql.ASTPipeFork, *googlesql.ASTPipeIf,
		*googlesql.ASTPipeIfCase, *googlesql.ASTPipeRecursiveUnion:
		if isNilNode(p) {
			return false
		}
		return pipeOpHasPredecessorOutputAlias(snap, p, name)
	default:
		return false
	}
}

func pipeParentHasOutputAlias(snap *DocumentSnapshot, parent googlesql.ASTNode, startIdx int, name string) (matched, stopped bool) {
	for i := startIdx; i >= 0; i-- {
		op, ok := astChild(parent, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(op) {
			continue
		}

		if m, s := pipeOpOutputAliasState(snap, op, name); s {
			return m, true
		}
	}

	return false, false
}

func pipeOpOutputAliasState(snap *DocumentSnapshot, op googlesql.ASTPipeOperatorNode, name string) (matched, stopped bool) {
	switch p := op.(type) {
	case *googlesql.ASTPipeSelect:
		return pipeSelectAliasState(snap, p, name)
	case *googlesql.ASTPipeAggregate:
		return pipeAggregateAliasState(snap, p, name)
	case *googlesql.ASTPipeExtend:
		return pipeSelectClauseAliasState(snap, p.Select, name)
	case *googlesql.ASTPipeWindow:
		return pipeSelectClauseAliasState(snap, p.Select, name)
	case *googlesql.ASTPipeDrop:
		return pipeDropAliasState(p, name)
	case *googlesql.ASTPipeRename:
		return pipeRenameAliasState(p, name)
	case *googlesql.ASTPipePivot:
		return pipePivotAliasState(p, name)
	case *googlesql.ASTPipeUnpivot:
		return pipeUnpivotAliasState(p, name)
	case *googlesql.ASTPipeIf:
		return pipeIfAliasState(snap, p, name)
	case *googlesql.ASTPipeJoin:
		j, _ := p.Join()
		if tableExprHasColumn(snap, p, j, name) {
			return true, true
		}
		return false, false
	case *googlesql.ASTPipeRecursiveUnion:
		return false, false
	case *googlesql.ASTPipeCall, *googlesql.ASTPipeMatchRecognize:
		return false, true
	default:
		return false, false
	}
}

func pipeIfAliasState(snap *DocumentSnapshot, p *googlesql.ASTPipeIf, name string) (matched, stopped bool) {
	if isNilNode(p) {
		return false, false
	}

	allStopped := true
	for i := range astNumChildren(p) {
		ic, ok := astChild(p, i).(*googlesql.ASTPipeIfCase)
		if !ok || isNilNode(ic) {
			continue
		}
		sub, _ := ic.Subpipeline()
		m, s := subpipelineOutputAliasState(snap, sub, name)
		if m {
			return true, true
		}
		if !s {
			allStopped = false
		}
	}

	elseSub, _ := p.ElseSubpipeline()
	hasElse := !isNilNode(elseSub)
	if hasElse {
		m, s := subpipelineOutputAliasState(snap, elseSub, name)
		if m {
			return true, true
		}
		if !s {
			allStopped = false
		}
	}

	if hasElse && allStopped {
		return false, true
	}

	return false, false
}

func subpipelineOutputAliasState(snap *DocumentSnapshot, sub *googlesql.ASTSubpipeline, name string) (matched, stopped bool) {
	if isNilNode(sub) {
		return false, false
	}

	return pipeParentHasOutputAlias(snap, sub, astNumChildren(sub)-1, name)
}

func pipeSelectAliasState(snap *DocumentSnapshot, p *googlesql.ASTPipeSelect, name string) (matched, stopped bool) {
	if isNilNode(p) {
		return false, true
	}
	sel, _ := p.Select()
	if selectListHasAlias(snap, sel, name) {
		return true, true
	}

	return selectListPassThroughPipeState(snap, p, sel, name)
}

func selectListPassThroughPipeState(
	snap *DocumentSnapshot,
	pipeSel *googlesql.ASTPipeSelect,
	sel *googlesql.ASTSelect,
	name string,
) (matched, stopped bool) {
	if isNilNode(sel) {
		return false, true
	}
	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return false, true
	}

	n := astNumChildren(sl)
	for i := range n {
		col, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(col) || !isNilNode(matchASTAliasIdent(col.Alias())) {
			continue
		}
		expr, _ := col.Expression()
		if isUnqualifiedStarAllowing(expr, name) || isSingleIdentPathWithName(expr, name) {
			return false, false
		}
		if qualParts := dotStarQualifierParts(expr); len(qualParts) > 0 && starModifiersAllowPassThrough(starModifiersOfExpr(expr), name) {
			if dotStarQualifierPathHasColumn(snap, pipeSel, qualParts, name) {
				return true, true
			}
		}
	}

	return false, true
}

func dotStarQualifierParts(expr googlesql.ASTNode) []string {
	var qual googlesql.ASTNode
	switch node := expr.(type) {
	case *googlesql.ASTDotStar:
		if !isNilNode(node) {
			qual, _ = node.Expr()
		}
	case *googlesql.ASTDotStarWithModifiers:
		if !isNilNode(node) {
			qual, _ = node.Expr()
		}
	default:
		return nil
	}

	pe, ok := qual.(*googlesql.ASTPathExpression)
	if !ok || isNilNode(pe) {
		return nil
	}

	parts, err := pe.ToIdentifierVector()
	if err != nil || len(parts) == 0 {
		return nil
	}

	return parts
}

func isUnqualifiedStarAllowing(expr googlesql.ASTNode, name string) bool {
	switch node := expr.(type) {
	case *googlesql.ASTStar:
		return !isNilNode(node)
	case *googlesql.ASTStarWithModifiers:
		if isNilNode(node) {
			return false
		}
		mods, _ := node.Modifiers()
		return starModifiersAllowPassThrough(mods, name)
	default:
		return false
	}
}

func dotStarQualifierPathHasColumn(snap *DocumentSnapshot, op googlesql.ASTNode, qualParts []string, name string) bool {
	if ifCase, ok := op.(*googlesql.ASTPipeIfCase); ok && !isNilNode(ifCase) {
		op = astParent(ifCase)
	}
	if isNilNode(op) || len(qualParts) == 0 {
		return false
	}

	parent := astParent(op)
	if isNilNode(parent) {
		return false
	}

	idx := childIndexInParent(parent, op)
	for i := idx - 1; i >= 0; i-- {
		prevOp, ok := astChild(parent, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(prevOp) {
			continue
		}
		if matched, stopped := pipeOpDotStarQualPathState(snap, prevOp, qualParts, name); stopped {
			return matched
		}
	}

	switch p := parent.(type) {
	case *googlesql.ASTQuery:
		qe, _ := p.QueryExpr()
		return queryExprHasQualPathColumn(snap, p, qe, qualParts, name)
	case *googlesql.ASTSubpipeline:
		return subpipelineDotStarQualPathHasCol(snap, p, qualParts, name)
	default:
		return false
	}
}

func pipeOpDotStarQualPathState(
	snap *DocumentSnapshot,
	op googlesql.ASTPipeOperatorNode,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	switch p := op.(type) {
	case *googlesql.ASTPipeAs:
		if id := matchASTAliasIdent(p.Alias()); !isNilNode(id) && matchesIdentName(id, qualParts[0]) {
			return pipeAliasQualHasCol(snap, p, qualParts, name), true
		}
	case *googlesql.ASTPipeDrop:
		cl, _ := p.ColumnList()
		if identifierListHasName(cl, qualParts[0]) {
			return false, true
		}
	case *googlesql.ASTPipeRename:
		return pipeRenameDotStarQualState(snap, p, qualParts, name)
	case *googlesql.ASTPipePivot:
		return pipePivotDotStarQualState(snap, p, qualParts, name)
	case *googlesql.ASTPipeUnpivot:
		return pipeUnpivotDotStarQualState(snap, p, qualParts, name)
	case *googlesql.ASTPipeIf:
		return pipeIfDotStarQualState(snap, p, qualParts, name)
	case *googlesql.ASTPipeJoin:
		j, _ := p.Join()
		if tableExprHasQualPathColumn(snap, p, j, qualParts, name) {
			return true, true
		}
		if tableExprBindsAlias(j, qualParts[0]) {
			return false, true
		}
	case *googlesql.ASTPipeSelect:
		return pipeSelectDotStarQualState(snap, p, qualParts, name)
	case *googlesql.ASTPipeExtend:
		return pipeExtendDotStarQualState(snap, p, p.Select, qualParts, name)
	case *googlesql.ASTPipeWindow:
		return pipeExtendDotStarQualState(snap, p, p.Select, qualParts, name)
	case *googlesql.ASTPipeAggregate, *googlesql.ASTPipeSetOperation,
		*googlesql.ASTPipeCall, *googlesql.ASTPipeMatchRecognize:
		return false, true
	default:
	}

	return false, false
}

func pipeRenameDotStarQualState(
	snap *DocumentSnapshot,
	p *googlesql.ASTPipeRename,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if isNilNode(p) || len(qualParts) == 0 {
		return false, false
	}

	head := qualParts[0]
	renamedAway := false
	for i := range astNumChildren(p) {
		item, ok := astChild(p, i).(*googlesql.ASTPipeRenameItem)
		if !ok || isNilNode(item) {
			continue
		}
		if newID, _ := item.NewName(); matchesIdentName(newID, head) {
			return pipeRenameMatchedItemDotStarQual(snap, p, item, qualParts, name), true
		}
		if oldID, _ := item.OldName(); matchesIdentName(oldID, head) {
			renamedAway = true
		}
	}

	return false, renamedAway
}

func pipeRenameMatchedItemDotStarQual(
	snap *DocumentSnapshot,
	p *googlesql.ASTPipeRename,
	item *googlesql.ASTPipeRenameItem,
	qualParts []string,
	name string,
) bool {
	oldID, _ := item.OldName()
	if isNilNode(oldID) {
		return false
	}
	oldName, err := oldID.GetAsString()
	if err != nil || oldName == "" {
		return false
	}
	nextQual := appendQualParts([]string{oldName}, qualParts[1:])
	if len(nextQual) == 1 && !pipeOpHasPredecessorOutputAlias(snap, p, oldName) {
		return false
	}

	return dotStarQualifierPathHasColumn(snap, p, nextQual, name)
}

func pipeSelectDotStarQualState(
	snap *DocumentSnapshot,
	p *googlesql.ASTPipeSelect,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if isNilNode(p) || len(qualParts) == 0 {
		return false, true
	}
	sel, _ := p.Select()
	if isNilNode(sel) {
		return false, true
	}
	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return false, true
	}

	for i := range astNumChildren(sl) {
		col, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(col) {
			continue
		}
		if m, hit := pipeSelectColDotStarQual(snap, p, col, qualParts, name); hit {
			return m, true
		}
	}

	return false, true
}

func pipeExtendDotStarQualState(
	snap *DocumentSnapshot,
	pipeOp googlesql.ASTNode,
	selFn func() (*googlesql.ASTSelect, error),
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if isNilNode(pipeOp) || len(qualParts) == 0 {
		return false, false
	}
	sel, err := selFn()
	if err != nil || isNilNode(sel) {
		return false, false
	}
	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return false, false
	}

	for i := range astNumChildren(sl) {
		col, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(col) || isNilNode(matchASTAliasIdent(col.Alias())) {
			continue
		}
		if m, hit := pipeSelectColDotStarQual(snap, pipeOp, col, qualParts, name); hit {
			return m, true
		}
	}

	return false, false
}

func pipeSelectColDotStarQual(
	snap *DocumentSnapshot,
	pipeSel googlesql.ASTNode,
	col *googlesql.ASTSelectColumn,
	qualParts []string,
	name string,
) (matched, hit bool) {
	head := qualParts[0]
	expr, _ := col.Expression()
	if aliasID := matchASTAliasIdent(col.Alias()); !isNilNode(aliasID) {
		return pipeSelectAliasedColDotStarQual(snap, pipeSel, aliasID, expr, qualParts, name)
	}

	mods := starModifiersOfExpr(expr)
	if isUnqualifiedStarAllowing(expr, head) {
		if repExpr, replaced := starModifiersReplaceExpr(mods, head); replaced {
			return pipeSelectReplacedColDotStarQual(snap, pipeSel, repExpr, qualParts, name), true
		}
		if pipeOpHasPredecessorOutputAlias(snap, pipeSel, head) &&
			dotStarQualifierPathHasColumn(snap, pipeSel, qualParts, name) {
			return true, true
		}
		return false, false
	}

	if dsParts := dotStarQualifierParts(expr); len(dsParts) > 0 && starModifiersAllowPassThrough(mods, head) {
		if repExpr, replaced := starModifiersReplaceExpr(mods, head); replaced {
			return pipeSelectReplacedColDotStarQual(snap, pipeSel, repExpr, qualParts, name), true
		}
		if dotStarQualifierPathHasColumn(snap, pipeSel, appendQualParts(dsParts, qualParts), name) {
			return true, true
		}
		return false, false
	}

	if pathParts := pathExprParts(expr); len(pathParts) > 0 && strings.EqualFold(pathParts[len(pathParts)-1], head) {
		if len(pathParts) == 1 && !pipeOpHasPredecessorOutputAlias(snap, pipeSel, pathParts[0]) {
			return false, false
		}
		return dotStarQualifierPathHasColumn(snap, pipeSel, appendQualParts(pathParts, qualParts[1:]), name), true
	}

	return false, false
}

func pipeSelectAliasedColDotStarQual(
	snap *DocumentSnapshot,
	pipeSel googlesql.ASTNode,
	aliasID *googlesql.ASTIdentifier,
	expr googlesql.ASTNode,
	qualParts []string,
	name string,
) (matched, hit bool) {
	if !matchesIdentName(aliasID, qualParts[0]) {
		return false, false
	}

	return pipeSelectReplacedColDotStarQual(snap, pipeSel, expr, qualParts, name), true
}

func pipeSelectReplacedColDotStarQual(
	snap *DocumentSnapshot,
	pipeSel, expr googlesql.ASTNode,
	qualParts []string,
	name string,
) bool {
	pathParts := pathExprParts(expr)
	if len(pathParts) == 0 {
		return false
	}
	if len(pathParts) == 1 && !pipeOpHasPredecessorOutputAlias(snap, pipeSel, pathParts[0]) {
		return false
	}

	return dotStarQualifierPathHasColumn(snap, pipeSel, appendQualParts(pathParts, qualParts[1:]), name)
}

func pathExprParts(expr googlesql.ASTNode) []string {
	pe, ok := expr.(*googlesql.ASTPathExpression)
	if !ok || isNilNode(pe) {
		return nil
	}

	parts, err := pe.ToIdentifierVector()
	if err != nil || len(parts) == 0 {
		return nil
	}

	return parts
}

func appendQualParts(prefix, suffix []string) []string {
	out := make([]string, 0, len(prefix)+len(suffix))
	out = append(out, prefix...)
	out = append(out, suffix...)

	return out
}

func pipeAliasQualHasCol(snap *DocumentSnapshot, op googlesql.ASTNode, qualParts []string, name string) bool {
	if len(qualParts) == 1 {
		return pipeOpHasPredecessorOutputAlias(snap, op, name)
	}

	return pipeOpHasPredecessorOutputAlias(snap, op, qualParts[1]) &&
		dotStarQualifierPathHasColumn(snap, op, qualParts[1:], name)
}

func pipePivotDotStarQualState(
	snap *DocumentSnapshot,
	p *googlesql.ASTPipePivot,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if id := matchPipePivotAlias(p, qualParts[0]); !isNilNode(id) {
		if len(qualParts) == 1 {
			if m, stopped := pipePivotAliasState(p, name); stopped {
				return m, true
			}
			return pipeOpHasPredecessorOutputAlias(snap, p, name), true
		}
		if m, stopped := pipePivotAliasState(p, qualParts[1]); stopped && !m {
			return false, true
		}
		return pipeAliasQualHasCol(snap, p, qualParts, name), true
	}

	if pipePivotConsumesColumn(p, qualParts[0]) || !pipeOpHasPredecessorOutputAlias(snap, p, qualParts[0]) {
		return false, true
	}

	return false, false
}

func pipeUnpivotDotStarQualState(
	snap *DocumentSnapshot,
	p *googlesql.ASTPipeUnpivot,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if id := matchPipeUnpivotAlias(p, qualParts[0]); !isNilNode(id) {
		if len(qualParts) == 1 {
			if m, stopped := pipeUnpivotAliasState(p, name); stopped {
				return m, true
			}
			return pipeOpHasPredecessorOutputAlias(snap, p, name), true
		}
		if m, stopped := pipeUnpivotAliasState(p, qualParts[1]); stopped && !m {
			return false, true
		}
		return pipeAliasQualHasCol(snap, p, qualParts, name), true
	}

	if pipeUnpivotConsumesColumn(p, qualParts[0]) || !pipeOpHasPredecessorOutputAlias(snap, p, qualParts[0]) {
		return false, true
	}

	return false, false
}

func pipeIfDotStarQualState(
	snap *DocumentSnapshot,
	p *googlesql.ASTPipeIf,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if isNilNode(p) {
		return false, false
	}

	allStopped := true
	for i := range astNumChildren(p) {
		ic, ok := astChild(p, i).(*googlesql.ASTPipeIfCase)
		if !ok || isNilNode(ic) {
			continue
		}
		sub, _ := ic.Subpipeline()
		m, s := subpipelineDotStarQualState(snap, sub, qualParts, name)
		if m {
			return true, true
		}
		if !s {
			allStopped = false
		}
	}

	elseSub, _ := p.ElseSubpipeline()
	hasElse := !isNilNode(elseSub)
	if hasElse {
		m, s := subpipelineDotStarQualState(snap, elseSub, qualParts, name)
		if m {
			return true, true
		}
		if !s {
			allStopped = false
		}
	}

	if hasElse && allStopped {
		return false, true
	}

	return false, false
}

func subpipelineDotStarQualState(
	snap *DocumentSnapshot,
	sub *googlesql.ASTSubpipeline,
	qualParts []string,
	name string,
) (matched, stopped bool) {
	if isNilNode(sub) {
		return false, false
	}

	for i := astNumChildren(sub) - 1; i >= 0; i-- {
		op, ok := astChild(sub, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(op) {
			continue
		}
		if m, s := pipeOpDotStarQualPathState(snap, op, qualParts, name); s {
			return m, true
		}
	}

	return false, false
}

func queryExprHasQualPathColumn(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	qe googlesql.ASTQueryExpressionNode,
	qualParts []string,
	name string,
) bool {
	for !isNilNode(qe) {
		switch node := qe.(type) {
		case *googlesql.ASTSelect:
			return selectProjectsQualPathColumn(snap, node, qualParts, name)
		case *googlesql.ASTFromQuery:
			fc, _ := node.FromClause()
			return fromClauseHasQualPathColumn(snap, scopeNode, fc, qualParts, name)
		case *googlesql.ASTQuery:
			return queryHasQualPathColumn(snap, node, qualParts, name)
		case *googlesql.ASTSetOperation:
			inp, _ := node.Inputs(0)
			qe = inp
		default:
			return false
		}
	}

	return false
}

func selectProjectsQualPathColumn(
	snap *DocumentSnapshot,
	sel *googlesql.ASTSelect,
	qualParts []string,
	name string,
) bool {
	if isNilNode(sel) || len(qualParts) == 0 {
		return false
	}
	fc, _ := sel.FromClause()
	if isNilNode(fc) {
		return false
	}
	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return false
	}

	for i := range astNumChildren(sl) {
		col, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(col) {
			continue
		}
		if matched, done := selectColProjectsQualPath(snap, sel, fc, col, qualParts, name); done {
			return matched
		}
	}

	return false
}

func selectColProjectsQualPath(
	snap *DocumentSnapshot,
	sel *googlesql.ASTSelect,
	fc *googlesql.ASTFromClause,
	col *googlesql.ASTSelectColumn,
	qualParts []string,
	name string,
) (matched, done bool) {
	head := qualParts[0]
	expr, _ := col.Expression()
	if aliasID := matchASTAliasIdent(col.Alias()); !isNilNode(aliasID) {
		if !matchesIdentName(aliasID, head) {
			return false, false
		}
		return selectReplacedColProjectsQualPath(snap, sel, fc, expr, qualParts, name), true
	}

	mods := starModifiersOfExpr(expr)
	if isUnqualifiedStarAllowing(expr, head) {
		if repExpr, replaced := starModifiersReplaceExpr(mods, head); replaced {
			return selectReplacedColProjectsQualPath(snap, sel, fc, repExpr, qualParts, name), true
		}
		if fromClauseHasQualPathColumn(snap, sel, fc, qualParts, name) {
			return true, true
		}
		return false, false
	}

	if dsParts := dotStarQualifierParts(expr); len(dsParts) > 0 && starModifiersAllowPassThrough(mods, head) {
		if repExpr, replaced := starModifiersReplaceExpr(mods, head); replaced {
			return selectReplacedColProjectsQualPath(snap, sel, fc, repExpr, qualParts, name), true
		}
		if fromClauseHasQualPathColumn(snap, sel, fc, appendQualParts(dsParts, qualParts), name) {
			return true, true
		}
		return false, false
	}

	if pathParts := pathExprParts(expr); len(pathParts) > 0 && strings.EqualFold(pathParts[len(pathParts)-1], head) {
		if fromClauseHasQualPathColumn(snap, sel, fc, appendQualParts(pathParts, qualParts[1:]), name) {
			return true, true
		}
	}

	return false, false
}

func selectReplacedColProjectsQualPath(
	snap *DocumentSnapshot,
	sel *googlesql.ASTSelect,
	fc *googlesql.ASTFromClause,
	expr googlesql.ASTNode,
	qualParts []string,
	name string,
) bool {
	pathParts := pathExprParts(expr)
	if len(pathParts) == 0 {
		return false
	}

	return fromClauseHasQualPathColumn(snap, sel, fc, appendQualParts(pathParts, qualParts[1:]), name)
}

func queryHasQualPathColumn(snap *DocumentSnapshot, q *googlesql.ASTQuery, qualParts []string, name string) bool {
	if isNilNode(q) {
		return false
	}

	n := astNumChildren(q)
	for i := n - 1; i >= 0; i-- {
		op, ok := astChild(q, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(op) {
			continue
		}
		if matched, stopped := pipeOpDotStarQualPathState(snap, op, qualParts, name); stopped {
			return matched
		}
	}

	qe, _ := q.QueryExpr()

	return queryExprHasQualPathColumn(snap, q, qe, qualParts, name)
}

func subpipelineDotStarQualPathHasCol(
	snap *DocumentSnapshot,
	sub *googlesql.ASTSubpipeline,
	qualParts []string,
	name string,
) bool {
	switch p := astParent(sub).(type) {
	case *googlesql.ASTSubpipelineStatement:
		swp, ok := astParent(p).(*googlesql.ASTStatementWithPipeOperators)
		if !ok || isNilNode(swp) {
			return false
		}
		stmt, _ := swp.Statement()
		if qs, ok := stmt.(*googlesql.ASTQueryStatement); ok && !isNilNode(qs) {
			q, _ := qs.Query()
			return queryHasQualPathColumn(snap, q, qualParts, name)
		}
		return false
	case *googlesql.ASTPipeTee, *googlesql.ASTPipeFork, *googlesql.ASTPipeIf,
		*googlesql.ASTPipeIfCase, *googlesql.ASTPipeRecursiveUnion:
		return dotStarQualifierPathHasColumn(snap, p, qualParts, name)
	default:
		return false
	}
}

func pipeAggregateAliasState(snap *DocumentSnapshot, p *googlesql.ASTPipeAggregate, name string) (matched, stopped bool) {
	if isNilNode(p) {
		return false, true
	}
	sel, _ := p.Select()
	if selectListHasAlias(snap, sel, name) || groupByHasExplicitOrDottedAlias(sel, name) {
		return true, true
	}
	if groupByHasBareColumn(sel, name) {
		return false, false
	}

	return false, true
}

func pipeDropAliasState(p *googlesql.ASTPipeDrop, name string) (matched, stopped bool) {
	if !isNilNode(p) {
		cl, _ := p.ColumnList()
		if identifierListHasName(cl, name) {
			return false, true
		}
	}

	return false, false
}

func pipePivotAliasState(p *googlesql.ASTPipePivot, name string) (matched, stopped bool) {
	if pipePivotHasAlias(p, name) {
		return true, true
	}
	if pipePivotConsumesColumn(p, name) {
		return false, true
	}

	return false, false
}

func pipeUnpivotAliasState(p *googlesql.ASTPipeUnpivot, name string) (matched, stopped bool) {
	if pipeUnpivotHasOutputColumn(p, name) {
		return true, true
	}
	if pipeUnpivotConsumesColumn(p, name) {
		return false, true
	}

	return false, false
}

func pipePivotConsumesColumn(p *googlesql.ASTPipePivot, name string) bool {
	if isNilNode(p) {
		return false
	}

	pc, _ := p.PivotClause()
	if isNilNode(pc) {
		return false
	}

	forExpr, _ := pc.ForExpression()
	if isSingleIdentPathWithName(forExpr, name) {
		return true
	}

	peList, _ := pc.PivotExpressions()
	if isNilNode(peList) {
		return false
	}

	matched := false
	walkASTNodes(peList, func(n googlesql.ASTNode) bool {
		if matched {
			return false
		}
		if isSingleIdentPathWithName(n, name) {
			matched = true
			return false
		}
		return true
	})

	return matched
}

func pipeUnpivotHasOutputColumn(p *googlesql.ASTPipeUnpivot, name string) bool {
	if isNilNode(p) {
		return false
	}

	uc, _ := p.UnpivotClause()
	if isNilNode(uc) {
		return false
	}

	nameCol, _ := uc.UnpivotOutputNameColumn()
	if isSingleIdentPathWithName(nameCol, name) {
		return true
	}

	valCols, _ := uc.UnpivotOutputValueColumns()
	if isNilNode(valCols) {
		return false
	}

	n := astNumChildren(valCols)
	for i := range n {
		if isSingleIdentPathWithName(astChild(valCols, i), name) {
			return true
		}
	}

	return false
}

func pipeUnpivotConsumesColumn(p *googlesql.ASTPipeUnpivot, name string) bool {
	if isNilNode(p) {
		return false
	}

	uc, _ := p.UnpivotClause()
	if isNilNode(uc) {
		return false
	}

	inItems, _ := uc.UnpivotInItems()
	if isNilNode(inItems) {
		return false
	}

	n := astNumChildren(inItems)
	for i := range n {
		item, ok := astChild(inItems, i).(*googlesql.ASTUnpivotInItem)
		if !ok || isNilNode(item) {
			continue
		}
		cols, _ := item.UnpivotColumns()
		if isNilNode(cols) {
			continue
		}
		m := astNumChildren(cols)
		for j := range m {
			if isSingleIdentPathWithName(astChild(cols, j), name) {
				return true
			}
		}
	}

	return false
}

func pipePivotHasAlias(p *googlesql.ASTPipePivot, name string) bool {
	if isNilNode(p) {
		return false
	}

	pc, _ := p.PivotClause()
	if isNilNode(pc) {
		return false
	}

	pvl, _ := pc.PivotValues()
	if isNilNode(pvl) {
		return false
	}

	aggAliases, hasUnaliasedAgg := collectPivotExprAliases(pc)
	n := astNumChildren(pvl)
	for i := range n {
		pv, ok := astChild(pvl, i).(*googlesql.ASTPivotValue)
		if !ok || isNilNode(pv) {
			continue
		}
		if pivotValueMatchesOutputName(pv, aggAliases, hasUnaliasedAgg, name) {
			return true
		}
	}

	return false
}

func collectPivotExprAliases(pc *googlesql.ASTPivotClause) (aggAliases []string, hasUnaliased bool) {
	peList, _ := pc.PivotExpressions()
	if isNilNode(peList) {
		return nil, true
	}

	n := astNumChildren(peList)
	if n == 0 {
		return nil, true
	}

	for i := range n {
		pe, ok := astChild(peList, i).(*googlesql.ASTPivotExpression)
		if !ok || isNilNode(pe) {
			continue
		}
		id := matchASTAliasIdent(pe.Alias())
		if isNilNode(id) {
			hasUnaliased = true
			continue
		}
		if alName, err := id.GetAsString(); err == nil && alName != "" {
			aggAliases = append(aggAliases, alName)
		}
	}

	return aggAliases, hasUnaliased
}

func pivotValueMatchesOutputName(pv *googlesql.ASTPivotValue, aggAliases []string, hasUnaliasedAgg bool, name string) bool {
	id := matchASTAliasIdent(pv.Alias())
	if isNilNode(id) {
		return false
	}

	valName, err := id.GetAsString()
	if err != nil || valName == "" {
		return false
	}

	if hasUnaliasedAgg && strings.EqualFold(valName, name) {
		return true
	}

	for _, agg := range aggAliases {
		if strings.EqualFold(agg+"_"+valName, name) {
			return true
		}
	}

	return false
}

func pipeSelectClauseAliasState(snap *DocumentSnapshot, selFn func() (*googlesql.ASTSelect, error), name string) (matched, stopped bool) {
	sel, err := selFn()
	if err == nil && selectListHasAlias(snap, sel, name) {
		return true, true
	}

	return false, false
}

func groupByHasExplicitOrDottedAlias(sel *googlesql.ASTSelect, name string) bool {
	if isNilNode(sel) {
		return false
	}

	gb, _ := sel.GroupBy()
	if isNilNode(gb) {
		return false
	}

	n := astNumChildren(gb)
	for i := range n {
		item, ok := astChild(gb, i).(*googlesql.ASTGroupingItem)
		if !ok || isNilNode(item) {
			continue
		}
		if id := matchASTAliasIdent(item.Alias()); !isNilNode(id) && matchesIdentName(id, name) {
			return true
		}
		expr, _ := item.Expression()
		if isDottedPathWithLeaf(expr, name) {
			return true
		}
	}

	return false
}

func groupByHasBareColumn(sel *googlesql.ASTSelect, name string) bool {
	if isNilNode(sel) {
		return false
	}

	gb, _ := sel.GroupBy()
	if isNilNode(gb) {
		return false
	}

	n := astNumChildren(gb)
	for i := range n {
		item, ok := astChild(gb, i).(*googlesql.ASTGroupingItem)
		if !ok || isNilNode(item) {
			continue
		}
		if id := matchASTAliasIdent(item.Alias()); !isNilNode(id) {
			continue
		}
		if groupingItemHasBareColumn(item, name) {
			return true
		}
	}

	return false
}

func groupingItemHasBareColumn(item *googlesql.ASTGroupingItem, name string) bool {
	expr, _ := item.Expression()
	if isSingleIdentPathWithName(expr, name) {
		return true
	}

	found := false
	walkASTNodes(item, func(n googlesql.ASTNode) bool {
		if found {
			return false
		}
		if pe, ok := n.(*googlesql.ASTPathExpression); ok && !isNilNode(pe) {
			if isBareGroupByItemExpr(pe) && matchesPathLeaf(pe, name) {
				found = true
				return false
			}
		}
		return true
	})

	return found
}

func isSingleIdentPathWithName(expr googlesql.ASTNode, name string) bool {
	pe, ok := expr.(*googlesql.ASTPathExpression)
	if !ok || isNilNode(pe) {
		return false
	}

	num, _ := pe.NumNames()

	return num == 1 && matchesPathLeaf(pe, name)
}

func isDottedPathWithLeaf(expr googlesql.ASTNode, name string) bool {
	pe, ok := expr.(*googlesql.ASTPathExpression)
	if !ok || isNilNode(pe) {
		return false
	}

	num, _ := pe.NumNames()

	return num >= 2 && matchesPathLeaf(pe, name)
}

func matchesPathLeaf(pe *googlesql.ASTPathExpression, name string) bool {
	last, _ := pe.LastName()

	return matchesIdentName(last, name)
}

func identifierListHasName(il *googlesql.ASTIdentifierList, name string) bool {
	if isNilNode(il) {
		return false
	}

	n := astNumChildren(il)
	for i := range n {
		id, ok := astChild(il, i).(*googlesql.ASTIdentifier)
		if ok && !isNilNode(id) && matchesIdentName(id, name) {
			return true
		}
	}

	return false
}

func pipeRenameAliasState(p *googlesql.ASTPipeRename, name string) (matched, stopped bool) {
	if isNilNode(p) {
		return false, false
	}

	renamedAway := false
	n := astNumChildren(p)
	for i := range n {
		item, ok := astChild(p, i).(*googlesql.ASTPipeRenameItem)
		if !ok || isNilNode(item) {
			continue
		}
		if newID, _ := item.NewName(); matchesIdentName(newID, name) {
			return true, true
		}
		if oldID, _ := item.OldName(); matchesIdentName(oldID, name) {
			renamedAway = true
		}
	}

	return false, renamedAway
}

func selectListHasAlias(snap *DocumentSnapshot, sel *googlesql.ASTSelect, name string) bool {
	if isNilNode(sel) {
		return false
	}

	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return false
	}

	n := astNumChildren(sl)
	for i := range n {
		col, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(col) {
			continue
		}
		if id := matchASTAliasIdent(col.Alias()); !isNilNode(id) && matchesIdentName(id, name) {
			return true
		}
		expr, _ := col.Expression()
		if isNilNode(matchASTAliasIdent(col.Alias())) && isDottedPathWithLeaf(expr, name) {
			if dottedSelectExprExists(snap, sel, expr, name) {
				return true
			}
		}
		if starModifiersHasReplaceAlias(starModifiersOfExpr(expr), name) {
			return true
		}
	}

	return false
}

func dottedSelectExprExists(snap *DocumentSnapshot, sel *googlesql.ASTSelect, expr googlesql.ASTNode, name string) bool {
	pe, ok := expr.(*googlesql.ASTPathExpression)
	if !ok || isNilNode(pe) {
		return false
	}
	parts, err := pe.ToIdentifierVector()
	if err != nil || len(parts) < 2 {
		return false
	}

	fc, _ := sel.FromClause()
	if !isNilNode(fc) {
		te, _ := fc.TableExpression()
		matched, _ := tableExprDottedColState(snap, sel, te, parts, name)
		return matched
	}

	if pipeSel, ok := astParent(sel).(*googlesql.ASTPipeSelect); ok && !isNilNode(pipeSel) {
		return pipeSelectUpstreamHasDottedCol(snap, pipeSel, parts, name)
	}

	return true
}

func tableExprDottedColState(
	snap *DocumentSnapshot,
	scopeNode, te googlesql.ASTNode,
	parts []string,
	name string,
) (matched, known bool) {
	if isNilNode(te) || len(parts) < 2 {
		return false, true
	}

	switch node := te.(type) {
	case *googlesql.ASTTableSubquery:
		return tableSubqueryDottedColState(snap, node, parts, name)
	case *googlesql.ASTTablePathExpression:
		return tablePathDottedColState(snap, scopeNode, node, parts, name)
	case *googlesql.ASTJoin:
		lhs, _ := node.Lhs()
		rhs, _ := node.Rhs()
		mL, kL := tableExprDottedColState(snap, scopeNode, lhs, parts, name)
		if mL {
			return true, true
		}
		mR, kR := tableExprDottedColState(snap, scopeNode, rhs, parts, name)
		return mR, kL && kR
	case *googlesql.ASTParenthesizedJoin:
		j, _ := node.Join()
		return tableExprDottedColState(snap, scopeNode, j, parts, name)
	case *googlesql.ASTUnnestExpressionWithOptAliasAndOffset:
		if id := matchASTAliasIdent(node.OptionalAlias()); !isNilNode(id) && matchesIdentName(id, parts[0]) {
			return true, false
		}
		return false, false
	case *googlesql.ASTTVF:
		if id := matchASTAliasIdent(node.Alias()); !isNilNode(id) && matchesIdentName(id, parts[0]) {
			return true, false
		}
		return false, false
	default:
		return false, false
	}
}

func tableSubqueryDottedColState(
	snap *DocumentSnapshot,
	node *googlesql.ASTTableSubquery,
	parts []string,
	name string,
) (matched, known bool) {
	sub, _ := node.Subquery()
	if id := matchASTAliasIdent(node.Alias()); !isNilNode(id) && matchesIdentName(id, parts[0]) {
		if len(parts) == 2 {
			return queryHasOutputAlias(snap, sub, name), true
		}
		return queryHasOutputAlias(snap, sub, parts[1]) && queryHasQualPathColumn(snap, sub, parts[1:len(parts)-1], name), true
	}

	return queryHasOutputAlias(snap, sub, parts[0]) && queryHasQualPathColumn(snap, sub, parts[:len(parts)-1], name), true
}

func tablePathDottedColState(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	tp *googlesql.ASTTablePathExpression,
	parts []string,
	name string,
) (matched, known bool) {
	if cteQuery := resolveEnclosingCTEQuery(scopeNode, tp); !isNilNode(cteQuery) {
		if tablePathMatchesAlias(tp, parts[0]) {
			if len(parts) == 2 {
				return queryHasOutputAlias(snap, cteQuery, name), true
			}
			return queryHasOutputAlias(snap, cteQuery, parts[1]) && queryHasQualPathColumn(snap, cteQuery, parts[1:len(parts)-1], name), true
		}
		return queryHasOutputAlias(snap, cteQuery, parts[0]) && queryHasQualPathColumn(snap, cteQuery, parts[:len(parts)-1], name), true
	}

	var meta *catalog.TableMeta
	if snap != nil {
		meta = tableMetaFromTablePath(snap, tp)
	}
	if meta == nil {
		return tablePathMatchesAlias(tp, parts[0]), false
	}

	if tablePathMatchesAlias(tp, parts[0]) {
		if len(parts) == 2 {
			return tableMetaHasColumn(meta, name), true
		}
		return tableMetaHasStructFieldPath(meta, parts[1:len(parts)-1], name), true
	}

	return tableMetaHasStructFieldPath(meta, parts[:len(parts)-1], name), true
}

func pipeSelectUpstreamHasDottedCol(
	snap *DocumentSnapshot,
	pipeSel *googlesql.ASTPipeSelect,
	parts []string,
	name string,
) bool {
	if dotStarQualifierPathHasColumn(snap, pipeSel, parts[:len(parts)-1], name) {
		return true
	}

	return pipeUpstreamAliasUnknownSchema(snap, pipeSel, parts[0])
}

func pipeUpstreamAliasUnknownSchema(snap *DocumentSnapshot, op googlesql.ASTNode, aliasName string) bool {
	if ifCase, ok := op.(*googlesql.ASTPipeIfCase); ok && !isNilNode(ifCase) {
		op = astParent(ifCase)
	}
	if isNilNode(op) {
		return false
	}

	parent := astParent(op)
	if isNilNode(parent) {
		return false
	}

	for i := childIndexInParent(parent, op) - 1; i >= 0; i-- {
		prevOp, ok := astChild(parent, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(prevOp) {
			continue
		}
		if unk, stopped := pipeOpAliasUnknownSchemaState(snap, prevOp, aliasName); stopped {
			return unk
		}
	}

	switch p := parent.(type) {
	case *googlesql.ASTQuery:
		qe, _ := p.QueryExpr()
		return queryExprBindsUnknownAlias(snap, p, qe, aliasName)
	case *googlesql.ASTSubpipeline:
		return subpipelineAliasUnknownSchema(snap, p, aliasName)
	default:
		return false
	}
}

func pipeOpAliasUnknownSchemaState(snap *DocumentSnapshot, op googlesql.ASTPipeOperatorNode, aliasName string) (unk, stopped bool) {
	switch p := op.(type) {
	case *googlesql.ASTPipeAs:
		if id := matchASTAliasIdent(p.Alias()); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return false, true
		}
	case *googlesql.ASTPipePivot:
		if !isNilNode(matchPipePivotAlias(p, aliasName)) {
			return false, true
		}
	case *googlesql.ASTPipeUnpivot:
		if !isNilNode(matchPipeUnpivotAlias(p, aliasName)) {
			return false, true
		}
	case *googlesql.ASTPipeJoin:
		j, _ := p.Join()
		if unk, found := tableExprAliasUnknownState(snap, p, j, aliasName); found {
			return unk, true
		}
	case *googlesql.ASTPipeSelect, *googlesql.ASTPipeAggregate, *googlesql.ASTPipeSetOperation,
		*googlesql.ASTPipeCall, *googlesql.ASTPipeMatchRecognize:
		return false, true
	default:
	}

	return false, false
}

func queryExprBindsUnknownAlias(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	qe googlesql.ASTQueryExpressionNode,
	aliasName string,
) bool {
	for !isNilNode(qe) {
		switch node := qe.(type) {
		case *googlesql.ASTFromQuery:
			fc, _ := node.FromClause()
			if isNilNode(fc) {
				return false
			}
			te, _ := fc.TableExpression()
			unk, _ := tableExprAliasUnknownState(snap, scopeNode, te, aliasName)
			return unk
		case *googlesql.ASTQuery:
			return queryBindsUnknownAlias(snap, node, aliasName)
		case *googlesql.ASTSetOperation:
			inp, _ := node.Inputs(0)
			qe = inp
		default:
			return false
		}
	}

	return false
}

func queryBindsUnknownAlias(snap *DocumentSnapshot, q *googlesql.ASTQuery, aliasName string) bool {
	if isNilNode(q) {
		return false
	}
	for i := astNumChildren(q) - 1; i >= 0; i-- {
		op, ok := astChild(q, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(op) {
			continue
		}
		if unk, stopped := pipeOpAliasUnknownSchemaState(snap, op, aliasName); stopped {
			return unk
		}
	}
	qe, _ := q.QueryExpr()
	return queryExprBindsUnknownAlias(snap, q, qe, aliasName)
}

func subpipelineAliasUnknownSchema(snap *DocumentSnapshot, sub *googlesql.ASTSubpipeline, aliasName string) bool {
	switch p := astParent(sub).(type) {
	case *googlesql.ASTSubpipelineStatement:
		swp, ok := astParent(p).(*googlesql.ASTStatementWithPipeOperators)
		if !ok || isNilNode(swp) {
			return false
		}
		stmt, _ := swp.Statement()
		if qs, ok := stmt.(*googlesql.ASTQueryStatement); ok && !isNilNode(qs) {
			q, _ := qs.Query()
			return queryBindsUnknownAlias(snap, q, aliasName)
		}
		return false
	case *googlesql.ASTPipeTee, *googlesql.ASTPipeFork, *googlesql.ASTPipeIf,
		*googlesql.ASTPipeIfCase, *googlesql.ASTPipeRecursiveUnion:
		return pipeUpstreamAliasUnknownSchema(snap, p, aliasName)
	default:
		return false
	}
}

func tableExprAliasUnknownState(snap *DocumentSnapshot, scopeNode, te googlesql.ASTNode, aliasName string) (unk, found bool) {
	if isNilNode(te) {
		return false, false
	}
	switch node := te.(type) {
	case *googlesql.ASTTablePathExpression:
		return tablePathAliasUnknownState(snap, scopeNode, node, aliasName)
	case *googlesql.ASTTableSubquery:
		return aliasMatchesIdent(node.Alias())(aliasName)
	case *googlesql.ASTUnnestExpressionWithOptAliasAndOffset:
		if id := matchASTAliasIdent(node.OptionalAlias()); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return true, true
		}
	case *googlesql.ASTTVF:
		if id := matchASTAliasIdent(node.Alias()); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return true, true
		}
	case *googlesql.ASTJoin:
		lhs, _ := node.Lhs()
		if unk, found := tableExprAliasUnknownState(snap, scopeNode, lhs, aliasName); found {
			return unk, true
		}
		rhs, _ := node.Rhs()
		return tableExprAliasUnknownState(snap, scopeNode, rhs, aliasName)
	case *googlesql.ASTParenthesizedJoin:
		j, _ := node.Join()
		return tableExprAliasUnknownState(snap, scopeNode, j, aliasName)
	default:
	}

	return false, false
}

func tablePathAliasUnknownState(
	snap *DocumentSnapshot,
	scopeNode googlesql.ASTNode,
	tp *googlesql.ASTTablePathExpression,
	aliasName string,
) (unk, found bool) {
	if !tablePathMatchesAlias(tp, aliasName) {
		return false, false
	}
	if cte := resolveEnclosingCTEQuery(scopeNode, tp); !isNilNode(cte) {
		return false, true
	}
	if snap != nil && tableMetaFromTablePath(snap, tp) != nil {
		return false, true
	}

	return true, true
}

func aliasMatchesIdent(al *googlesql.ASTAlias, err error) func(string) (unk, found bool) {
	return func(aliasName string) (unk, found bool) {
		if id := matchASTAliasIdent(al, err); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return false, true
		}

		return false, false
	}
}

func tableExprBindsAlias(te googlesql.ASTNode, aliasName string) bool {
	if isNilNode(te) {
		return false
	}
	switch node := te.(type) {
	case *googlesql.ASTTablePathExpression:
		return tablePathMatchesAlias(node, aliasName)
	case *googlesql.ASTTableSubquery:
		return !isNilNode(matchASTAliasIdent(node.Alias())) && matchesIdentName(matchASTAliasIdent(node.Alias()), aliasName)
	case *googlesql.ASTUnnestExpressionWithOptAliasAndOffset:
		return !isNilNode(matchASTAliasIdent(node.OptionalAlias())) && matchesIdentName(matchASTAliasIdent(node.OptionalAlias()), aliasName)
	case *googlesql.ASTTVF:
		return !isNilNode(matchASTAliasIdent(node.Alias())) && matchesIdentName(matchASTAliasIdent(node.Alias()), aliasName)
	case *googlesql.ASTJoin:
		lhs, _ := node.Lhs()
		rhs, _ := node.Rhs()
		return tableExprBindsAlias(lhs, aliasName) || tableExprBindsAlias(rhs, aliasName)
	case *googlesql.ASTParenthesizedJoin:
		j, _ := node.Join()
		return tableExprBindsAlias(j, aliasName)
	default:
		return false
	}
}

func starModifiersOfExpr(expr googlesql.ASTNode) *googlesql.ASTStarModifiers {
	switch node := expr.(type) {
	case *googlesql.ASTStarWithModifiers:
		if isNilNode(node) {
			return nil
		}
		mods, _ := node.Modifiers()
		return mods
	case *googlesql.ASTDotStarWithModifiers:
		if isNilNode(node) {
			return nil
		}
		mods, _ := node.Modifiers()
		return mods
	default:
		return nil
	}
}

func starModifiersAllowPassThrough(mods *googlesql.ASTStarModifiers, name string) bool {
	if isNilNode(mods) {
		return true
	}

	exceptList, _ := mods.ExceptList()
	if isNilNode(exceptList) {
		return true
	}

	n := astNumChildren(exceptList)
	for i := range n {
		id, ok := astChild(exceptList, i).(*googlesql.ASTIdentifier)
		if ok && !isNilNode(id) && matchesIdentName(id, name) {
			return false
		}
	}

	return true
}

func starModifiersReplaceExpr(mods *googlesql.ASTStarModifiers, name string) (googlesql.ASTNode, bool) {
	if isNilNode(mods) {
		return nil, false
	}

	n := astNumChildren(mods)
	for i := range n {
		item, ok := astChild(mods, i).(*googlesql.ASTStarReplaceItem)
		if !ok || isNilNode(item) {
			continue
		}
		al, _ := item.Alias()
		if !isNilNode(al) && matchesIdentName(al, name) {
			expr, _ := item.Expression()
			return expr, true
		}
	}

	return nil, false
}

func starModifiersHasReplaceAlias(mods *googlesql.ASTStarModifiers, name string) bool {
	_, ok := starModifiersReplaceExpr(mods, name)

	return ok
}

func resolvedColumnRefDef(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	idNode *googlesql.ASTIdentifier,
	name string,
) ([]Location, bool) {
	rsList := resolvedStmtList(ctx.st)
	if len(rsList) == 0 || isDotStarQualifier(pe) {
		return nil, false
	}

	rs, colID, hasColID, matchedCR, isOffsetCol := classifyResolvedColumnRefInListWithID(rsList, idNode)
	if !matchedCR {
		return resolvedOrderOrGroupColDef(ctx, rsList, pe, idNode, name)
	}

	if isOffsetCol {
		if loc, ok := findEnclosingWithOffsetDefinition(ctx, pe, name); ok {
			return []Location{loc}, true
		}
	}

	if hasColID && isColumnRefScriptSymbol(ctx, rs, colID, pe, name) {
		return nil, false
	}

	return []Location{}, true
}

func isBareOrderByItemExpr(pe *googlesql.ASTPathExpression) bool {
	oe, ok := astParent(pe).(*googlesql.ASTOrderingExpression)
	if !ok || isNilNode(oe) {
		return false
	}

	expr, _ := oe.Expression()

	return isSameASTNode(expr, pe)
}

func isBareGroupByItemExpr(pe *googlesql.ASTPathExpression) bool {
	for cur := googlesql.ASTNode(pe); !isNilNode(cur); {
		switch p := astParent(cur).(type) {
		case *googlesql.ASTGroupingItem:
			return !isNilNode(p)
		case *googlesql.ASTRollup, *googlesql.ASTCube, *googlesql.ASTGroupingSet, *googlesql.ASTGroupingSetList:
			cur = p
		case *googlesql.ASTStructConstructorWithParens:
			if _, ok := astParent(p).(*googlesql.ASTGroupingSet); !ok {
				return false
			}
			cur = p
		default:
			return false
		}
	}

	return false
}

func isPatternOrGraphVarRef(pe *googlesql.ASTPathExpression, name string) bool {
	if isMatchRecognizePatternVarRef(pe, name) {
		return true
	}

	gq := enclosingGraphQuery(pe)

	return !isNilNode(gq) && graphQueryHasElementVar(gq, name)
}

func isUpdateSetTargetPath(pe *googlesql.ASTPathExpression) bool {
	cur := googlesql.ASTNode(pe)
	for !isNilNode(cur) {
		next, isTarget, done := stepUpdateSetTargetParent(cur)
		if done {
			return isTarget
		}
		cur = next
	}

	return false
}

func stepUpdateSetTargetParent(cur googlesql.ASTNode) (next googlesql.ASTNode, isTarget, done bool) {
	switch p := astParent(cur).(type) {
	case *googlesql.ASTUpdateSetValue:
		if isNilNode(p) {
			return nil, false, true
		}
		path, _ := p.Path()
		return nil, isSameASTNode(path, cur), true
	case *googlesql.ASTArrayElement:
		return matchChildExprParent(p, p.Array, cur)
	case *googlesql.ASTDotIdentifier:
		return matchChildExprParent(p, p.Expr, cur)
	case *googlesql.ASTDotGeneralizedField:
		return matchChildExprParent(p, p.Expr, cur)
	default:
		return nil, false, true
	}
}

func matchChildExprParent(
	parent googlesql.ASTNode,
	exprFn func() (googlesql.ASTExpressionNode, error),
	cur googlesql.ASTNode,
) (next googlesql.ASTNode, isTarget, done bool) {
	if isNilNode(parent) {
		return nil, false, true
	}

	expr, _ := exprFn()
	if isSameASTNode(expr, cur) {
		return parent, false, false
	}

	return nil, false, true
}

func definitionFromSetTargetQualifier(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	name string,
) []Location {
	if loc, ok := findDMLSetTargetAliasDefinition(ctx, pe, name); ok {
		return []Location{loc}
	}

	return []Location{}
}

func findDMLSetTargetAliasDefinition(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	aliasName string,
) (Location, bool) {
	for cur := astParent(pe); !isNilNode(cur); cur = astParent(cur) {
		switch stmt := cur.(type) {
		case *googlesql.ASTUpdateStatement:
			al, alErr := stmt.Alias()
			if id := matchDMLTargetAliasIdent(al, alErr, stmt.GetTargetPathForNonNested, aliasName, false); !isNilNode(id) {
				return locationForASTNode(ctx, id)
			}

			return Location{}, false
		case *googlesql.ASTMergeStatement:
			al, alErr := stmt.Alias()
			if id := matchDMLTargetAliasIdent(al, alErr, stmt.TargetPath, aliasName, false); !isNilNode(id) {
				return locationForASTNode(ctx, id)
			}

			return Location{}, false
		case *googlesql.ASTInsertStatement:
			if id := matchDMLTargetAliasIdent(nil, nil, stmt.GetTargetPathForNonNested, aliasName, false); !isNilNode(id) {
				return locationForASTNode(ctx, id)
			}

			return Location{}, false
		default:
		}
	}

	return Location{}, false
}

func isRepairedDanglingDotPath(pe *googlesql.ASTPathExpression) bool {
	num, _ := pe.NumNames()
	if num < 2 {
		return false
	}

	last, _ := pe.LastName()

	return matchesIdentName(last, phantomCursorIdent)
}

func matchDMLTargetAliasIdent(
	al *googlesql.ASTAlias,
	alErr error,
	targetPathFn func() (*googlesql.ASTPathExpression, error),
	aliasName string,
	explicitOnly bool,
) *googlesql.ASTIdentifier {
	if id := matchASTAliasIdent(al, alErr); !isNilNode(id) {
		if matchesIdentName(id, aliasName) {
			return id
		}

		return nil
	}

	if !explicitOnly && targetPathFn != nil {
		if tp, _ := targetPathFn(); !isNilNode(tp) {
			if last, _ := tp.LastName(); matchesIdentName(last, aliasName) {
				return last
			}
		}
	}

	return nil
}

func isDMLTargetPath(peParent googlesql.ASTNode, pe *googlesql.ASTPathExpression) bool {
	if isNilNode(peParent) || isNilNode(pe) {
		return false
	}

	switch stmt := peParent.(type) {
	case *googlesql.ASTInsertStatement:
		tp, _ := stmt.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTUpdateStatement:
		tp, _ := stmt.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTDeleteStatement:
		tp, _ := stmt.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTMergeStatement:
		tp, _ := stmt.TargetPath()
		return isSameASTNode(tp, pe)
	case *googlesql.ASTTruncateStatement:
		tp, _ := stmt.TargetPath()
		return isSameASTNode(tp, pe)
	default:
		return false
	}
}

func canResolveRangeVarAlias(
	st *AnalyzedStatement,
	pe *googlesql.ASTPathExpression,
	idNode *googlesql.ASTIdentifier,
) bool {
	if isDotStarQualifier(pe) {
		return true
	}

	rsList := resolvedStmtList(st)
	if len(rsList) == 0 {
		return true
	}

	if isRangeVarMakeStructInList(rsList, idNode) {
		return true
	}

	return len(st.InnerResults) > len(rsList) && !isResolvedColumnRefInList(rsList, idNode)
}

func isDotStarQualifier(pe *googlesql.ASTPathExpression) bool {
	switch parent := astParent(pe).(type) {
	case *googlesql.ASTDotStar:
		if !isNilNode(parent) {
			expr, _ := parent.Expr()
			return isSameASTNode(expr, pe)
		}
	case *googlesql.ASTDotStarWithModifiers:
		if !isNilNode(parent) {
			expr, _ := parent.Expr()
			return isSameASTNode(expr, pe)
		}
	default:
	}

	return false
}

func definitionFromTablePathExpr(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	name string,
) []Location {
	num, _ := pe.NumNames()
	if num == 1 {
		if loc, ok := findEnclosingCTEDefinition(ctx, pe, name); ok {
			return []Location{loc}
		}

		if locs := lookupTempTableDefinition(ctx.uri, ctx.snap, name); len(locs) > 0 {
			return locs
		}
	}

	// External catalog tables return empty locations without falling through to script symbols.
	return []Location{}
}

func definitionFromQualifierSegment(
	ctx defASTContext,
	pe *googlesql.ASTPathExpression,
	name string,
) []Location {
	if isMatchRecognizePatternVarRef(pe, name) {
		return []Location{}
	}

	if gq := enclosingGraphQuery(pe); !isNilNode(gq) && graphQueryHasElementVar(gq, name) {
		return []Location{}
	}

	if loc, ok := findEnclosingAliasDefinition(ctx, pe, name, false); ok {
		return []Location{loc}
	}

	if loc, ok := findEnclosingForInVarDefinition(ctx, pe, name); ok {
		return []Location{loc}
	}

	if isInsideFunctionDefBody(pe) {
		return []Location{}
	}

	if locs := lookupScriptSymbolDefinition(ctx.uri, ctx.snap, name, 0); len(locs) > 0 {
		return locs
	}

	return []Location{}
}

func isMatchRecognizePatternVarRef(pe *googlesql.ASTPathExpression, name string) bool {
	for cur := astParent(pe); !isNilNode(cur); cur = astParent(cur) {
		switch node := cur.(type) {
		case *googlesql.ASTMatchRecognizeClause:
			if isNilNode(node) {
				return false
			}
			inPatternScope := isNodeWithinSubtree(pe)(node.Measures()) ||
				isNodeWithinSubtree(pe)(node.PatternVariableDefinitionList()) ||
				isNodeWithinSubtree(pe)(node.Pattern())
			return inPatternScope && matchRecognizeHasPatternVar(node, name)
		case *googlesql.ASTQuery, *googlesql.ASTSelect, *googlesql.ASTFromQuery,
			*googlesql.ASTExpressionSubquery, *googlesql.ASTTableSubquery, *googlesql.ASTGraphTableQuery:
			return false
		default:
		}
	}

	return false
}

func isNodeWithinSubtree(target googlesql.ASTNode) func(googlesql.ASTNode, error) bool {
	return func(root googlesql.ASTNode, _ error) bool {
		if isNilNode(root) || isNilNode(target) {
			return false
		}

		rs, re, okR := astNodeByteRange(root)
		ts, te, okT := astNodeByteRange(target)

		return okR && okT && rs <= ts && te <= re
	}
}

func matchRecognizeHasPatternVar(mr *googlesql.ASTMatchRecognizeClause, name string) bool {
	if pvl, _ := mr.PatternVariableDefinitionList(); !isNilNode(pvl) {
		for i := range astNumChildren(pvl) {
			sc, ok := astChild(pvl, i).(*googlesql.ASTSelectColumn)
			if !ok || isNilNode(sc) {
				continue
			}
			if id := matchASTAliasIdent(sc.Alias()); matchesIdentName(id, name) {
				return true
			}
		}
	}

	pat, _ := mr.Pattern()
	if isNilNode(pat) {
		return false
	}

	found := false
	walkASTNodes(pat, func(n googlesql.ASTNode) bool {
		if found {
			return false
		}
		rv, ok := n.(*googlesql.ASTRowPatternVariable)
		if !ok || isNilNode(rv) {
			return true
		}
		if nm, _ := rv.Name(); matchesIdentName(nm, name) {
			found = true
			return false
		}
		return true
	})

	return found
}

func enclosingGraphQuery(node googlesql.ASTNode) googlesql.ASTNode {
	for cur := astParent(node); !isNilNode(cur); cur = astParent(cur) {
		switch cur.(type) {
		case *googlesql.ASTGraphTableQuery, *googlesql.ASTGqlGraphPatternQuery:
			return cur
		case *googlesql.ASTSelect, *googlesql.ASTFromQuery,
			*googlesql.ASTExpressionSubquery, *googlesql.ASTTableSubquery:
			return nil
		default:
		}
	}

	return nil
}

func graphQueryHasElementVar(gq googlesql.ASTNode, name string) bool {
	if isNilNode(gq) {
		return false
	}

	found := false
	walkASTNodes(gq, func(n googlesql.ASTNode) bool {
		if found {
			return false
		}
		if !isSameASTNode(n, gq) && isGraphQueryBarrierNode(n) {
			return false
		}
		if matchesGraphPatternVarNode(n, name) {
			found = true
			return false
		}
		return true
	})

	return found
}

func matchesGraphPatternVarNode(n googlesql.ASTNode, name string) bool {
	switch node := n.(type) {
	case *googlesql.ASTGraphElementPatternFiller:
		if !isNilNode(node) {
			v, _ := node.VariableName()
			return matchesIdentName(v, name)
		}
	case *googlesql.ASTGraphPathPattern:
		if !isNilNode(node) {
			p, _ := node.PathName()
			return matchesIdentName(p, name)
		}
	default:
	}

	return false
}

func isGraphQueryBarrierNode(n googlesql.ASTNode) bool {
	switch n.(type) {
	case *googlesql.ASTGraphTableQuery, *googlesql.ASTGqlGraphPatternQuery,
		*googlesql.ASTSelect, *googlesql.ASTFromQuery, *googlesql.ASTExpressionSubquery:
		return true
	default:
		return false
	}
}

func isSameASTNode(a, b googlesql.ASTNode) bool {
	if isNilNode(a) || isNilNode(b) {
		return false
	}

	sa, ea, oka := astNodeByteRange(a)
	sb, eb, okb := astNodeByteRange(b)

	return oka && okb && sa == sb && ea == eb
}

func findIdentifierAtOffset(root googlesql.ASTNode, off int) *googlesql.ASTIdentifier {
	var found *googlesql.ASTIdentifier
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if found != nil {
			return false
		}

		id, ok := n.(*googlesql.ASTIdentifier)
		if !ok || isNilNode(id) {
			return true
		}

		s, e, has := astNodeByteRange(id)
		if has && off >= s && off < e {
			found = id
			return false
		}

		return true
	})

	return found
}

func findEnclosingAliasDefinition(
	ctx defASTContext,
	fromNode googlesql.ASTNode,
	aliasName string,
	explicitOnly bool,
) (Location, bool) {
	fromStart, _, _ := astNodeByteRange(fromNode)
	for cur := fromNode; !isNilNode(cur); cur = astParent(cur) {
		if target := matchAliasInScopeNode(cur, fromStart, aliasName, explicitOnly); !isNilNode(target) {
			return locationForASTNode(ctx, target)
		}
	}

	return Location{}, false
}

func findEnclosingForInVarDefinition(
	ctx defASTContext,
	fromNode googlesql.ASTNode,
	varName string,
) (Location, bool) {
	for cur := astParent(fromNode); !isNilNode(cur); cur = astParent(cur) {
		forStmt, ok := cur.(*googlesql.ASTForInStatement)
		if !ok || isNilNode(forStmt) {
			continue
		}

		body, _ := forStmt.Body()
		if !isNodeWithinSubtree(fromNode)(body, nil) {
			continue
		}

		loopVar, _ := forStmt.Variable()
		if !isNilNode(loopVar) && matchesIdentName(loopVar, varName) {
			return locationForASTNode(ctx, loopVar)
		}
	}

	return Location{}, false
}

func matchAliasInScopeNode(scopeNode googlesql.ASTNode, fromStart int, aliasName string, explicitOnly bool) googlesql.ASTNode {
	switch scope := scopeNode.(type) {
	case *googlesql.ASTSelect:
		fc, _ := scope.FromClause()
		return findAliasDeclNodeInFrom(fc, fromStart, aliasName, explicitOnly)
	case *googlesql.ASTFromQuery:
		fc, _ := scope.FromClause()
		return findAliasDeclNodeInFrom(fc, fromStart, aliasName, explicitOnly)
	case *googlesql.ASTPipeJoin:
		j, _ := scope.Join()
		return findAliasDeclNodeInFrom(j, fromStart, aliasName, explicitOnly)
	case *googlesql.ASTPipeAs:
		if id := matchASTAliasIdent(scope.Alias()); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return id
		}
		return nil
	case *googlesql.ASTQuery:
		return matchQueryAlias(scope, fromStart, aliasName, explicitOnly)
	case *googlesql.ASTStatementWithPipeOperators:
		return matchStmtWithPipeAlias(scope, fromStart, aliasName, explicitOnly)
	case *googlesql.ASTUpdateStatement:
		al, alErr := scope.Alias()
		if id := matchDMLTargetAliasIdent(al, alErr, scope.GetTargetPathForNonNested, aliasName, explicitOnly); !isNilNode(id) {
			return id
		}
		fc, _ := scope.FromClause()
		return findAliasDeclNodeInFrom(fc, fromStart, aliasName, explicitOnly)
	case *googlesql.ASTDeleteStatement:
		if id := matchASTAliasIdent(scope.Alias()); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return id
		}
		return nil
	case *googlesql.ASTMergeStatement:
		return matchMergeAlias(scope, fromStart, aliasName, explicitOnly)
	default:
		return nil
	}
}

func matchQueryAlias(q *googlesql.ASTQuery, fromStart int, aliasName string, explicitOnly bool) googlesql.ASTNode {
	if isNilNode(q) {
		return nil
	}

	target, stopped := matchPipeChildrenAliases(q, fromStart, aliasName, explicitOnly)
	if !isNilNode(target) {
		return target
	}
	if stopped {
		return nil
	}

	qe, _ := q.QueryExpr()
	return matchQueryExprFromAlias(qe, fromStart, aliasName, explicitOnly)
}

func matchStmtWithPipeAlias(
	stmt *googlesql.ASTStatementWithPipeOperators,
	fromStart int,
	aliasName string,
	explicitOnly bool,
) googlesql.ASTNode {
	if isNilNode(stmt) {
		return nil
	}

	if suf, _ := stmt.PipeOperatorSuffix(); !isNilNode(suf) {
		if sub, _ := suf.Subpipeline(); !isNilNode(sub) {
			target, stopped := matchPipeChildrenAliases(sub, fromStart, aliasName, explicitOnly)
			if !isNilNode(target) {
				return target
			}
			if stopped {
				return nil
			}
		}
	}

	inner, _ := stmt.Statement()
	if qs, ok := inner.(*googlesql.ASTQueryStatement); ok && !isNilNode(qs) {
		q, _ := qs.Query()
		return matchQueryAlias(q, fromStart, aliasName, explicitOnly)
	}

	return nil
}

func matchPipeChildrenAliases(parent googlesql.ASTNode, fromStart int, aliasName string, explicitOnly bool) (googlesql.ASTNode, bool) {
	n := astNumChildren(parent)
	for i := n - 1; i >= 0; i-- {
		op, ok := astChild(parent, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(op) {
			continue
		}

		opS, opE, hasRange := astNodeByteRange(op)
		if hasRange && opS > fromStart {
			continue
		}

		target, isBarrier := matchPipeOpAlias(op, fromStart, aliasName, explicitOnly)
		if isBarrier && hasRange && opE > fromStart {
			continue
		}
		if !isNilNode(target) {
			return target, false
		}
		if isBarrier && hasRange && opE <= fromStart {
			return nil, true
		}
	}

	return nil, false
}

func matchPipeOpAlias(
	op googlesql.ASTPipeOperatorNode,
	fromStart int,
	aliasName string,
	explicitOnly bool,
) (target googlesql.ASTNode, isBarrier bool) {
	switch p := op.(type) {
	case *googlesql.ASTPipeAs:
		return matchAliasIfEqual(p.Alias())(aliasName), true
	case *googlesql.ASTPipeJoin:
		j, _ := p.Join()
		return findAliasDeclNodeInFrom(j, fromStart, aliasName, explicitOnly), false
	case *googlesql.ASTPipePivot:
		return matchPipePivotAlias(p, aliasName), true
	case *googlesql.ASTPipeUnpivot:
		return matchPipeUnpivotAlias(p, aliasName), true
	case *googlesql.ASTPipeCall:
		return matchPipeCallAlias(p, aliasName), true
	case *googlesql.ASTPipeMatchRecognize:
		return matchPipeMatchRecognizeAlias(p, aliasName), true
	case *googlesql.ASTPipeRecursiveUnion:
		return matchAliasIfEqual(p.Alias())(aliasName), true
	case *googlesql.ASTPipeSelect, *googlesql.ASTPipeAggregate, *googlesql.ASTPipeSetOperation:
		return nil, true
	default:
		return nil, false
	}
}

func matchPipeMatchRecognizeAlias(p *googlesql.ASTPipeMatchRecognize, aliasName string) googlesql.ASTNode {
	if mr, _ := p.MatchRecognize(); !isNilNode(mr) {
		return matchAliasIfEqual(mr.OutputAlias())(aliasName)
	}

	return nil
}

func matchAliasIfEqual(al *googlesql.ASTAlias, err error) func(string) googlesql.ASTNode {
	return func(aliasName string) googlesql.ASTNode {
		if id := matchASTAliasIdent(al, err); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return id
		}

		return nil
	}
}

func matchPipePivotAlias(p *googlesql.ASTPipePivot, aliasName string) googlesql.ASTNode {
	if pc, _ := p.PivotClause(); !isNilNode(pc) {
		return matchAliasIfEqual(pc.OutputAlias())(aliasName)
	}

	return nil
}

func matchPipeUnpivotAlias(p *googlesql.ASTPipeUnpivot, aliasName string) googlesql.ASTNode {
	if uc, _ := p.UnpivotClause(); !isNilNode(uc) {
		return matchAliasIfEqual(uc.OutputAlias())(aliasName)
	}

	return nil
}

func matchPipeCallAlias(p *googlesql.ASTPipeCall, aliasName string) googlesql.ASTNode {
	if tvf, _ := p.Tvf(); !isNilNode(tvf) {
		return matchAliasIfEqual(tvf.Alias())(aliasName)
	}

	return nil
}

func matchQueryExprFromAlias(qe googlesql.ASTQueryExpressionNode, fromStart int, aliasName string, explicitOnly bool) googlesql.ASTNode {
	switch qv := qe.(type) {
	case *googlesql.ASTFromQuery:
		if !isNilNode(qv) {
			fc, _ := qv.FromClause()
			return findAliasDeclNodeInFrom(fc, fromStart, aliasName, explicitOnly)
		}
	case *googlesql.ASTSelect:
		if !isNilNode(qv) {
			fc, _ := qv.FromClause()
			return findAliasDeclNodeInFrom(fc, fromStart, aliasName, explicitOnly)
		}
	default:
	}

	return nil
}

func matchMergeAlias(scope *googlesql.ASTMergeStatement, fromStart int, aliasName string, explicitOnly bool) googlesql.ASTNode {
	al, alErr := scope.Alias()
	if id := matchDMLTargetAliasIdent(al, alErr, scope.TargetPath, aliasName, explicitOnly); !isNilNode(id) {
		return id
	}

	te, _ := scope.TableExpression()

	return findAliasDeclNodeInFrom(te, fromStart, aliasName, explicitOnly)
}

func matchASTAliasIdent(al *googlesql.ASTAlias, err error) *googlesql.ASTIdentifier {
	if err != nil || isNilNode(al) {
		return nil
	}

	id, _ := al.Identifier()

	return id
}

func findAliasDeclNodeInFrom(root googlesql.ASTNode, fromStart int, aliasName string, explicitOnly bool) googlesql.ASTNode {
	if isNilNode(root) {
		return nil
	}

	var found googlesql.ASTNode
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if !isNilNode(found) {
			return false
		}

		target, descend := matchFromItemAlias(n, fromStart, aliasName, explicitOnly)
		if !isNilNode(target) {
			found = target
			return false
		}

		return descend
	})

	return found
}

func matchFromItemAlias(n googlesql.ASTNode, fromStart int, aliasName string, explicitOnly bool) (target googlesql.ASTNode, descend bool) {
	switch node := n.(type) {
	case *googlesql.ASTTablePathExpression:
		if target, shadowed := matchPostfixTableAlias(node, fromStart, aliasName); shadowed {
			return target, false
		}
		id, _ := matchTablePathAlias(node, aliasName, explicitOnly)
		return id, false
	case *googlesql.ASTTableSubquery:
		return matchTableExprExplicitAlias(node, node.Alias, fromStart, aliasName), false
	case *googlesql.ASTTVF:
		return matchTableExprExplicitAlias(node, node.Alias, fromStart, aliasName), false
	case *googlesql.ASTGraphTableQuery:
		return matchTableExprExplicitAlias(node, node.Alias, fromStart, aliasName), false
	case *googlesql.ASTParenthesizedJoin:
		if target, shadowed := matchPostfixTableAlias(node, fromStart, aliasName); shadowed {
			return target, false
		}
		j, _ := node.Join()
		return findAliasDeclNodeInFrom(j, fromStart, aliasName, explicitOnly), false
	case *googlesql.ASTUnnestExpressionWithOptAliasAndOffset:
		if id := matchASTAliasIdent(node.OptionalAlias()); !isNilNode(id) && matchesIdentName(id, aliasName) {
			return id, false
		}
		return nil, false
	case *googlesql.ASTOnClause, *googlesql.ASTUsingClause, *googlesql.ASTOnOrUsingClauseList,
		*googlesql.ASTHint, *googlesql.ASTUnnestExpression, *googlesql.ASTExpressionSubquery,
		*googlesql.ASTQuery, *googlesql.ASTSelect, *googlesql.ASTFromQuery, *googlesql.ASTSetOperation,
		*googlesql.ASTPivotClause, *googlesql.ASTUnpivotClause, *googlesql.ASTMatchRecognizeClause:
		return nil, false
	default:
		return nil, true
	}
}

func matchTableExprExplicitAlias(
	te googlesql.ASTNode,
	aliasFn func() (*googlesql.ASTAlias, error),
	fromStart int,
	aliasName string,
) googlesql.ASTNode {
	if target, shadowed := matchPostfixTableAlias(te, fromStart, aliasName); shadowed {
		return target
	}

	if id := matchASTAliasIdent(aliasFn()); !isNilNode(id) && matchesIdentName(id, aliasName) {
		return id
	}

	return nil
}

func matchPostfixTableAlias(te googlesql.ASTNode, fromStart int, aliasName string) (target googlesql.ASTNode, shadowed bool) {
	replOp := findReplacingPostfixOp(te)
	if isNilNode(replOp) {
		return nil, false
	}

	opS, opE, hasRange := astNodeByteRange(replOp)
	if hasRange && fromStart >= opS && fromStart < opE {
		return nil, false
	}

	if id := postfixOutputAliasIdent(replOp); !isNilNode(id) && matchesIdentName(id, aliasName) {
		return id, true
	}

	return nil, true
}

func findReplacingPostfixOp(tableExpr googlesql.ASTNode) googlesql.ASTNode {
	var found googlesql.ASTNode
	for i := range astNumChildren(tableExpr) {
		c := astChild(tableExpr, i)
		switch c.(type) {
		case *googlesql.ASTPivotClause, *googlesql.ASTUnpivotClause, *googlesql.ASTMatchRecognizeClause:
			found = c
		default:
		}
	}

	return found
}

func postfixOutputAliasIdent(op googlesql.ASTNode) *googlesql.ASTIdentifier {
	switch p := op.(type) {
	case *googlesql.ASTPivotClause:
		return matchASTAliasIdent(p.OutputAlias())
	case *googlesql.ASTUnpivotClause:
		return matchASTAliasIdent(p.OutputAlias())
	case *googlesql.ASTMatchRecognizeClause:
		return matchASTAliasIdent(p.OutputAlias())
	default:
		return nil
	}
}

func matchTablePathAlias(node *googlesql.ASTTablePathExpression, aliasName string, explicitOnly bool) (target googlesql.ASTNode, descend bool) {
	if id := matchASTAliasIdent(node.Alias()); !isNilNode(id) {
		if matchesIdentName(id, aliasName) {
			return id, false
		}

		return nil, true
	}

	if !explicitOnly {
		if pe, _ := node.PathExpr(); !isNilNode(pe) {
			if last, _ := pe.LastName(); matchesIdentName(last, aliasName) {
				return last, false
			}
		}
	}

	return nil, true
}

func matchesIdentName(id *googlesql.ASTIdentifier, target string) bool {
	if isNilNode(id) {
		return false
	}

	name, err := id.GetAsString()

	return err == nil && strings.EqualFold(name, target)
}

func findEnclosingWithOffsetDefinition(
	ctx defASTContext,
	fromNode googlesql.ASTNode,
	colName string,
) (Location, bool) {
	fromStart, _, _ := astNodeByteRange(fromNode)
	for cur := fromNode; !isNilNode(cur); cur = astParent(cur) {
		if target := matchWithOffsetInScopeNode(cur, fromStart, colName); !isNilNode(target) {
			return locationForASTNode(ctx, target)
		}
	}

	return Location{}, false
}

func matchWithOffsetInScopeNode(scopeNode googlesql.ASTNode, fromStart int, colName string) googlesql.ASTNode {
	switch scope := scopeNode.(type) {
	case *googlesql.ASTSelect:
		fc, _ := scope.FromClause()
		return findWithOffsetDeclInFrom(fc, fromStart, colName)
	case *googlesql.ASTFromQuery:
		fc, _ := scope.FromClause()
		return findWithOffsetDeclInFrom(fc, fromStart, colName)
	case *googlesql.ASTPipeJoin:
		j, _ := scope.Join()
		return findWithOffsetDeclInFrom(j, fromStart, colName)
	case *googlesql.ASTQuery:
		return matchQueryWithOffset(scope, fromStart, colName)
	case *googlesql.ASTStatementWithPipeOperators:
		return matchStmtWithPipeWithOffset(scope, fromStart, colName)
	case *googlesql.ASTUpdateStatement:
		if id := matchWithOffsetAliasIdent(scope.Offset()); !isNilNode(id) && matchesIdentName(id, colName) {
			return id
		}
		fc, _ := scope.FromClause()
		return findWithOffsetDeclInFrom(fc, fromStart, colName)
	case *googlesql.ASTDeleteStatement:
		if id := matchWithOffsetAliasIdent(scope.Offset()); !isNilNode(id) && matchesIdentName(id, colName) {
			return id
		}
		return nil
	default:
		return nil
	}
}

func matchQueryWithOffset(q *googlesql.ASTQuery, fromStart int, colName string) googlesql.ASTNode {
	if isNilNode(q) {
		return nil
	}

	target, stopped := matchPipeChildrenWithOffset(q, fromStart, colName)
	if !isNilNode(target) {
		return target
	}
	if stopped {
		return nil
	}

	qe, _ := q.QueryExpr()
	switch qv := qe.(type) {
	case *googlesql.ASTFromQuery:
		if !isNilNode(qv) {
			fc, _ := qv.FromClause()
			return findWithOffsetDeclInFrom(fc, fromStart, colName)
		}
	case *googlesql.ASTSelect:
		if !isNilNode(qv) {
			fc, _ := qv.FromClause()
			return findWithOffsetDeclInFrom(fc, fromStart, colName)
		}
	default:
	}

	return nil
}

func matchStmtWithPipeWithOffset(stmt *googlesql.ASTStatementWithPipeOperators, fromStart int, colName string) googlesql.ASTNode {
	if isNilNode(stmt) {
		return nil
	}

	if suf, _ := stmt.PipeOperatorSuffix(); !isNilNode(suf) {
		if sub, _ := suf.Subpipeline(); !isNilNode(sub) {
			target, stopped := matchPipeChildrenWithOffset(sub, fromStart, colName)
			if !isNilNode(target) {
				return target
			}
			if stopped {
				return nil
			}
		}
	}

	inner, _ := stmt.Statement()
	if qs, ok := inner.(*googlesql.ASTQueryStatement); ok && !isNilNode(qs) {
		q, _ := qs.Query()
		return matchQueryWithOffset(q, fromStart, colName)
	}

	return nil
}

func matchPipeChildrenWithOffset(parent googlesql.ASTNode, fromStart int, colName string) (googlesql.ASTNode, bool) {
	n := astNumChildren(parent)
	for i := n - 1; i >= 0; i-- {
		op, ok := astChild(parent, i).(googlesql.ASTPipeOperatorNode)
		if !ok || isNilNode(op) {
			continue
		}

		opS, opE, hasRange := astNodeByteRange(op)
		if hasRange && opS > fromStart {
			continue
		}

		switch p := op.(type) {
		case *googlesql.ASTPipeJoin:
			j, _ := p.Join()
			if target := findWithOffsetDeclInFrom(j, fromStart, colName); !isNilNode(target) {
				return target, false
			}
		case *googlesql.ASTPipeSelect, *googlesql.ASTPipeAggregate, *googlesql.ASTPipeSetOperation,
			*googlesql.ASTPipePivot, *googlesql.ASTPipeUnpivot, *googlesql.ASTPipeCall,
			*googlesql.ASTPipeMatchRecognize, *googlesql.ASTPipeRecursiveUnion:
			if hasRange && opE <= fromStart {
				return nil, true
			}
		default:
		}
	}

	return nil, false
}

func findWithOffsetDeclInFrom(root googlesql.ASTNode, fromStart int, colName string) googlesql.ASTNode {
	if isNilNode(root) {
		return nil
	}

	var found googlesql.ASTNode
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if !isNilNode(found) {
			return false
		}

		id, descend := matchFromItemWithOffset(n, fromStart, colName)
		if !isNilNode(id) {
			found = id
			return false
		}

		return descend
	})

	return found
}

func matchFromItemWithOffset(n googlesql.ASTNode, fromStart int, colName string) (googlesql.ASTNode, bool) {
	switch node := n.(type) {
	case *googlesql.ASTTablePathExpression:
		if _, shadowed := matchPostfixTableAlias(node, fromStart, ""); shadowed {
			return nil, false
		}
		if id := matchWithOffsetAliasIdent(node.WithOffset()); !isNilNode(id) && matchesIdentName(id, colName) {
			return id, false
		}
		return nil, false
	case *googlesql.ASTUnnestExpressionWithOptAliasAndOffset:
		if id := matchWithOffsetAliasIdent(node.OptionalWithOffset()); !isNilNode(id) && matchesIdentName(id, colName) {
			return id, false
		}
		return nil, false
	case *googlesql.ASTParenthesizedJoin:
		if _, shadowed := matchPostfixTableAlias(node, fromStart, ""); shadowed {
			return nil, false
		}
		return nil, true
	case *googlesql.ASTTableSubquery, *googlesql.ASTTVF, *googlesql.ASTGraphTableQuery,
		*googlesql.ASTOnClause, *googlesql.ASTUsingClause, *googlesql.ASTOnOrUsingClauseList,
		*googlesql.ASTHint, *googlesql.ASTUnnestExpression, *googlesql.ASTExpressionSubquery,
		*googlesql.ASTQuery, *googlesql.ASTSelect, *googlesql.ASTFromQuery, *googlesql.ASTSetOperation,
		*googlesql.ASTPivotClause, *googlesql.ASTUnpivotClause, *googlesql.ASTMatchRecognizeClause:
		return nil, false
	default:
		return nil, true
	}
}

func matchWithOffsetAliasIdent(wo *googlesql.ASTWithOffset, err error) *googlesql.ASTIdentifier {
	if err != nil || isNilNode(wo) {
		return nil
	}

	return matchASTAliasIdent(wo.Alias())
}

func findEnclosingCTEDefinition(
	ctx defASTContext,
	fromNode googlesql.ASTNode,
	cteName string,
) (Location, bool) {
	for cur := fromNode; !isNilNode(cur); cur = astParent(cur) {
		if q, ok := cur.(*googlesql.ASTQuery); ok && !isNilNode(q) {
			wc, _ := q.WithClause()
			if target := matchCTEInWithClause(wc, fromNode, cteName); !isNilNode(target) {
				return locationForASTNode(ctx, target)
			}
		}
	}

	return Location{}, false
}

func matchCTEInWithClause(wc *googlesql.ASTWithClause, scopeNode googlesql.ASTNode, cteName string) *googlesql.ASTIdentifier {
	if isNilNode(wc) {
		return nil
	}

	limit := astNumChildren(wc)
	if entryIdx := withClauseEntryIndexContaining(wc, scopeNode); entryIdx >= 0 {
		if rec, _ := wc.Recursive(); !rec {
			limit = entryIdx
		} else {
			limit = entryIdx + 1
		}
	}

	for i := range limit {
		entry, ok := astChild(wc, i).(*googlesql.ASTWithClauseEntry)
		if !ok || isNilNode(entry) {
			continue
		}

		aq, _ := entry.AliasedQuery()
		if isNilNode(aq) {
			continue
		}

		al, _ := aq.Alias()
		if matchesIdentName(al, cteName) {
			q, _ := aq.Query()
			if isNodeWithinSubtree(scopeNode)(q, nil) && !queryHasNonSelfBranch(q, scopeNode) {
				return nil
			}
			return al
		}
	}

	return nil
}

func lookupScriptSymbolDefinition(uri string, snap *DocumentSnapshot, name string, _ int) []Location {
	for _, dv := range snap.DeclaredVars {
		if strings.EqualFold(dv.Name, name) && dv.OrigSpan.End > dv.OrigSpan.Start {
			return []Location{{
				URI:   uri,
				Range: snap.LineIndex.RangeAt(dv.OrigSpan.Start, dv.OrigSpan.End),
			}}
		}
	}

	return lookupTempTableDefinition(uri, snap, name)
}

func lookupTempTableDefinition(uri string, snap *DocumentSnapshot, name string) []Location {
	for _, tt := range snap.TempTables {
		if strings.EqualFold(tt.Ref.Table, name) && tt.OrigSpan.End > tt.OrigSpan.Start {
			return []Location{{
				URI:   uri,
				Range: snap.LineIndex.RangeAt(tt.OrigSpan.Start, tt.OrigSpan.End),
			}}
		}
	}

	return nil
}

func locationForASTNode(ctx defASTContext, n googlesql.ASTNode) (Location, bool) {
	s, e, ok := astNodeByteRange(n)
	if !ok || s >= e {
		return Location{}, false
	}

	origS, origE, ok := ctx.unmap(s, e)
	if !ok || origS >= origE {
		return Location{}, false
	}

	os, oe, mapped := shadowSpanToOrig(ctx.snap.Projected.OffsetMap, ctx.st.ShadowSpan.Start+origS, ctx.st.ShadowSpan.Start+origE)
	if !mapped {
		return Location{}, false
	}

	return Location{
		URI:   ctx.uri,
		Range: ctx.snap.LineIndex.RangeAt(os, oe),
	}, true
}

// DocumentSymbols returns a hierarchical outline of symbols in uri.
func (e *Engine) DocumentSymbols(ctx context.Context, uri string) ([]DocumentSymbol, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	wasmMu.Lock()
	defer wasmMu.Unlock()

	symbols := make([]DocumentSymbol, 0)
	symbols = append(symbols, collectJinjaDocSymbols(snap)...)

	for _, st := range snap.Statements {
		if st.ParseErr == nil && !isNilNode(st.ASTStmt) {
			symbols = append(symbols, collectASTDocSymbols(snap, st, st.ASTStmt)...)
		}
	}

	sort.Slice(symbols, func(i, j int) bool {
		return posLess(symbols[i].Range.Start, symbols[j].Range.Start)
	})

	return symbols, nil
}

func collectJinjaDocSymbols(snap *DocumentSnapshot) []DocumentSymbol {
	var out []DocumentSymbol
	for _, sym := range scanJinjaSymbols(snap.Text, snap.Tokens) {
		if sym.kindLabel == "for" {
			continue
		}

		out = append(out, DocumentSymbol{
			Name:           sym.name,
			Detail:         "jinja " + sym.kindLabel,
			Kind:           sym.kind,
			Range:          snap.LineIndex.RangeAt(sym.fullStart, sym.fullEnd),
			SelectionRange: snap.LineIndex.RangeAt(sym.nameStart, sym.nameEnd),
		})
	}

	return out
}

func collectASTDocSymbols(snap *DocumentSnapshot, st *AnalyzedStatement, n googlesql.ASTNode) []DocumentSymbol {
	if isNilNode(n) {
		return nil
	}

	switch node := n.(type) {
	case *googlesql.ASTCreateTableStatement:
		nm, _ := node.Name()
		q, _ := node.Query()
		return singleDocSymbol(snap, st, node, nm, "TABLE", SymbolKindClass, collectASTDocSymbols(snap, st, q))
	case *googlesql.ASTCreateViewStatement:
		nm, _ := node.Name()
		q, _ := node.Query()
		return singleDocSymbol(snap, st, node, nm, "VIEW", SymbolKindInterface, collectASTDocSymbols(snap, st, q))
	case *googlesql.ASTCreateFunctionStatement:
		fd, _ := node.FunctionDeclaration()
		var nm *googlesql.ASTPathExpression
		if !isNilNode(fd) {
			nm, _ = fd.Name()
		}
		return singleDocSymbol(snap, st, node, nm, "FUNCTION", SymbolKindFunction, nil)
	case *googlesql.ASTCreateProcedureStatement:
		nm, _ := node.Name()
		body, _ := node.Body()
		return singleDocSymbol(snap, st, node, nm, "PROCEDURE", SymbolKindFunction, collectASTDocSymbols(snap, st, body))
	case *googlesql.ASTVariableDeclaration:
		return collectVarDeclSymbols(snap, st, node)
	case *googlesql.ASTBeginEndBlock:
		return collectBeginEndSymbols(snap, st, node)
	case *googlesql.ASTQuery:
		return collectQueryCTESymbols(snap, st, node)
	case *googlesql.ASTStatementWithPipeOperators:
		inner, _ := node.Statement()
		return collectASTDocSymbols(snap, st, inner)
	default:
		return collectChildrenDocSymbols(snap, st, n)
	}
}

func singleDocSymbol(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	fullNode googlesql.ASTNode,
	nameNode googlesql.ASTNode,
	detail string,
	kind SymbolKind,
	children []DocumentSymbol,
) []DocumentSymbol {
	if isNilNode(nameNode) {
		return children
	}

	name := extractNodeName(nameNode)
	if name == "" {
		return children
	}

	fullRange, okFull := rangeForASTNode(snap, st, fullNode)
	selRange, okSel := rangeForASTNode(snap, st, nameNode)
	if !okFull || !okSel {
		return children
	}

	return []DocumentSymbol{{
		Name:           name,
		Detail:         detail,
		Kind:           kind,
		Range:          fullRange,
		SelectionRange: selRange,
		Children:       children,
	}}
}

func collectVarDeclSymbols(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	decl *googlesql.ASTVariableDeclaration,
) []DocumentSymbol {
	vl, _ := decl.VariableList()
	if isNilNode(vl) {
		return nil
	}

	fullRange, okFull := rangeForASTNode(snap, st, decl)
	if !okFull {
		return nil
	}

	var out []DocumentSymbol
	n := astNumChildren(vl)
	for i := range n {
		id, ok := astChild(vl, i).(*googlesql.ASTIdentifier)
		if !ok || isNilNode(id) {
			continue
		}

		name, _ := id.GetAsString()
		selRange, okSel := rangeForASTNode(snap, st, id)
		if name != "" && okSel {
			out = append(out, DocumentSymbol{
				Name:           name,
				Detail:         "DECLARE",
				Kind:           SymbolKindVariable,
				Range:          fullRange,
				SelectionRange: selRange,
			})
		}
	}

	return out
}

func collectBeginEndSymbols(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	blk *googlesql.ASTBeginEndBlock,
) []DocumentSymbol {
	sl, _ := blk.StatementListNode()
	children := collectChildrenDocSymbols(snap, st, sl)

	hl, _ := blk.HandlerList()
	children = append(children, collectChildrenDocSymbols(snap, st, hl)...)

	label, _ := blk.Label()
	if !isNilNode(label) {
		if id, _ := label.Name(); !isNilNode(id) {
			return singleDocSymbol(snap, st, blk, id, "BEGIN...END", SymbolKindNamespace, children)
		}
	}

	if len(children) == 0 {
		return nil
	}

	fullRange, ok := rangeForASTNode(snap, st, blk)
	if !ok {
		return children
	}

	return []DocumentSymbol{{
		Name:           "BEGIN...END",
		Kind:           SymbolKindNamespace,
		Range:          fullRange,
		SelectionRange: fullRange,
		Children:       children,
	}}
}

func collectQueryCTESymbols(snap *DocumentSnapshot, st *AnalyzedStatement, q *googlesql.ASTQuery) []DocumentSymbol {
	var out []DocumentSymbol
	wc, _ := q.WithClause()
	if !isNilNode(wc) {
		n := astNumChildren(wc)
		for i := range n {
			entry, ok := astChild(wc, i).(*googlesql.ASTWithClauseEntry)
			if !ok || isNilNode(entry) {
				continue
			}

			aq, _ := entry.AliasedQuery()
			if isNilNode(aq) {
				continue
			}

			al, _ := aq.Alias()
			subQ, _ := aq.Query()
			children := collectASTDocSymbols(snap, st, subQ)
			out = append(out, singleDocSymbol(snap, st, aq, al, "CTE", SymbolKindStruct, children)...)
		}
	}

	qe, _ := q.QueryExpr()
	out = append(out, collectChildrenDocSymbols(snap, st, qe)...)

	return out
}

func collectChildrenDocSymbols(snap *DocumentSnapshot, st *AnalyzedStatement, n googlesql.ASTNode) []DocumentSymbol {
	if isNilNode(n) {
		return nil
	}

	var out []DocumentSymbol
	num := astNumChildren(n)
	for i := range num {
		out = append(out, collectASTDocSymbols(snap, st, astChild(n, i))...)
	}

	return out
}

func extractNodeName(n googlesql.ASTNode) string {
	switch v := n.(type) {
	case *googlesql.ASTIdentifier:
		s, _ := v.GetAsString()
		return s
	case *googlesql.ASTPathExpression:
		parts, _ := v.ToIdentifierVector()
		return strings.Join(parts, ".")
	default:
		return ""
	}
}

func rangeForASTNode(snap *DocumentSnapshot, st *AnalyzedStatement, n googlesql.ASTNode) (syntax.Range, bool) {
	s, e, ok := astNodeByteRange(n)
	if !ok || s >= e {
		return syntax.Range{}, false
	}

	os, oe, mapped := shadowSpanToOrig(snap.Projected.OffsetMap, st.ShadowSpan.Start+s, st.ShadowSpan.Start+e)
	if !mapped {
		return syntax.Range{}, false
	}

	return snap.LineIndex.RangeAt(os, oe), true
}
