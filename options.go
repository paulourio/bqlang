package bqlang

import "github.com/goccy/go-zetasql"

func DefaultParserOptions() *zetasql.ParserOptions {
	po := zetasql.NewParserOptions()

	po.SetLanguageOptions(DefaultLanguageOptions())

	return po
}

func DefaultLanguageOptions() *zetasql.LanguageOptions {
	lo := zetasql.NewLanguageOptions()

	lo.EnableLanguageFeature(zetasql.FeatureV13Qualify)
	lo.EnableLanguageFeature(zetasql.FeatureV13IsDistinct)
	lo.EnableLanguageFeature(zetasql.FeatureV13AllowConsecutiveOn)
	lo.EnableLanguageFeature(zetasql.FeatureJsonType)
	lo.EnableLanguageFeature(zetasql.FeatureV13AllowDashesInTableName)
	lo.EnableLanguageFeature(zetasql.FeatureCreateViewWithColumnList)
	// lo.EnableLanguageFeature(zetasql.FeatureJsonArrayFunctions)
	// lo.EnableLanguageFeature(zetasql.FeatureJsonLegacyParse)
	// lo.EnableLanguageFeature(zetasql.FeatureJsonNoValidation)
	// lo.EnableLanguageFeature(zetasql.FeatureJsonValueExtractionFunctions)

	return lo
}
