package formatter_test

import (
	"strings"
	"testing"

	"github.com/paulourio/bqlang/formatter"
)

const (
	benchSmallQuery = `select user_id, count(*) as total from analytics.events where event_date >= '2026-01-01' group by user_id order by total desc limit 100`

	benchComplexQuery = `
-- Monthly revenue cohort breakdown
with raw_orders as (
  select o.order_id, o.customer_id, date_trunc(o.created_at, month) as order_month,
         sum(oi.quantity * oi.unit_price) as gross_amount,
         countif(oi.discount_amount > 0) as discounted_items
  from sales.orders as o
  inner join sales.order_items as oi on o.order_id = oi.order_id
  left join sales.customers as c on o.customer_id = c.customer_id and c.is_active = true
  where o.status in ('PAID', 'SHIPPED') and o.created_at >= timestamp('2025-01-01')
  group by all
),
ranked as (
  select order_id, customer_id, order_month, gross_amount, discounted_items,
         row_number() over (partition by customer_id order by order_month desc) as rn,
         case when gross_amount >= 1000 then 'enterprise' when gross_amount >= 200 then 'mid' else 'smb' end as tier
  from raw_orders
)
select order_month, tier, count(distinct customer_id) as customers, sum(gross_amount) as revenue
from ranked
where rn <= 12
group by 1, 2
qualify rank() over (partition by order_month order by revenue desc) <= 5
order by order_month desc, revenue desc
`

	benchTemplatedQuery = `
{% set metrics = ['revenue', 'orders', 'refunds'] %}
{# Incremental daily rollup #}
select date_trunc(created_at, day) as event_day,
       region_code
       {% for m in metrics %}, sum({{ m }}) as total_{{ m }}{% endfor %}
  from {{ ref('stg_payments') }}
 where 1 = 1
{% if is_incremental() %}
   AND created_at > (SELECT MAX(event_day) FROM {{ this }})
{% endif %}
{% if include_test %} OR is_test = TRUE {% endif %}
 group by all
`
)

func buildLargeBenchScript(repeats int) string {
	var b strings.Builder
	b.Grow(repeats * (len(benchComplexQuery) + 4))

	for range repeats {
		b.WriteString(benchComplexQuery)
		b.WriteString(";\n")
	}

	return b.String()
}

func BenchmarkFormatSmallQuery(b *testing.B) {
	opts := formatter.DefaultPrintOptions()
	if _, err := formatter.Format(benchSmallQuery, opts); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := formatter.Format(benchSmallQuery, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatComplexQuery(b *testing.B) {
	opts := formatter.DefaultPrintOptions()
	if _, err := formatter.Format(benchComplexQuery, opts); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := formatter.Format(benchComplexQuery, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatTemplatedQuery(b *testing.B) {
	opts := formatter.DefaultPrintOptions()
	if _, err := formatter.Format(benchTemplatedQuery, opts); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := formatter.Format(benchTemplatedQuery, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatLargeScript(b *testing.B) {
	script := buildLargeBenchScript(20)
	opts := formatter.DefaultPrintOptions()

	if _, err := formatter.Format(script, opts); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := formatter.Format(script, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatIdempotentDoublePass(b *testing.B) {
	opts := formatter.DefaultPrintOptions()
	if _, err := formatter.Format(benchComplexQuery, opts); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		out1, err := formatter.Format(benchComplexQuery, opts)
		if err != nil {
			b.Fatal(err)
		}

		out2, err := formatter.Format(out1, opts)
		if err != nil {
			b.Fatal(err)
		}

		if out1 != out2 {
			b.Fatal("formatting is not idempotent")
		}
	}
}
