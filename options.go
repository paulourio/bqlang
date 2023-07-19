package bqlang

import "github.com/goccy/go-zetasql"

func DefaultParserOptions() *zetasql.ParserOptions {
	po := zetasql.NewParserOptions()

	po.SetLanguageOptions(DefaultLanguageOptions())

	return po
}

func DefaultLanguageOptions() *zetasql.LanguageOptions {
	lang := zetasql.NewLanguageOptions()

	lang.EnableLanguageFeature(zetasql.FeatureCreateExternalTableWithConnection)
	lang.EnableLanguageFeature(zetasql.FeatureCreateViewWithColumnList)
	lang.EnableLanguageFeature(zetasql.FeatureJsonType)
	lang.EnableLanguageFeature(zetasql.FeatureTemplateFunctions)
	lang.EnableLanguageFeature(zetasql.FeatureV13AllowConsecutiveOn)
	lang.EnableLanguageFeature(zetasql.FeatureV13AllowDashesInTableName)
	lang.EnableLanguageFeature(zetasql.FeatureV13IsDistinct)
	lang.EnableLanguageFeature(zetasql.FeatureV13Qualify)
	lang.EnableLanguageFeature(zetasql.FeatureV13RemoteFunction)

	return lang
}
