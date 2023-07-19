# bqlang - tooling for ZetaSQL

I develop this tooling mainly for use with BigQuery.

## Formatter - bqfmt

### Status support

#### Supported features

- [x] Qualify
- [x] `IS DISTINCT FROM`
- [x] Consecutive `ON ...`
- [x] `JSON` type

    The `JSON` type is partially supported is select column lists, but not in other places.

- [x] Allow dashes in table name (ie, `FROM project-name.dataset.table` without escaping)
- [x] Create view with column list (`CREATE VIEW vw(field1, field2)`)
- [x] Remote Functions

#### Types

- [x] Simple types:  BIGNUMERIC, BOOL, BYTES, BYTES, DATE, DATETIME, FLOAT64, INT64, INTERVAL, NUMERIC, STRING, TIME, TIMESTAMP
- [x] ARRAY
- [x] STRUCT
- [x] GEOGRAPHY
- [x] Templated types

    Not supported ideally, but it works on my machine.
    Currently, go-zetasql has a bug to parse these types but we have a workaround to make it work.

#### Literals

All literals for all types are supported.

#### Comments

Google's ZetaSQL parser ignores comments.
Current experimental implementation of `bqfmt` tries the best to format maintaining comments at the closest position possible from the input.
Formatting without comment is always idempotent, but formatting code preserving comments is not guaranteed to be idempotent.

#### Expressions

- [x] Field access operator (`expression.fieldname[...]`)
- [x] Array subscript operator (`array_expression[array_subscript_specifier]`)
- [x] JSON subscript operator (`json_expression[array_element_id]`, `json_expression[field_name]`)
- [x] Arithmetic operators (`X + Y`, `X - Y`, `X * Y`, `X / Y`, `+X`, `-X`).
- [x] Bitwise operators (`~X`,  `X | Y`, `X ^ Y`, `X & Y`, `X << Y`, `X >> Y`)
- [x] Comparison operators (`=`, `!=`, `<>`, `>`, `<`, `>=`, `<=`, `[NOT] LIKE`, `IS [NOT]`, `IN`, `IS [NOT] DISTINCT FROM`).
- [x] Conditional expressions (`CASE`, `COALESCE`, `IF`, `IFNULL`, `NULLIF`, )
- [x] Logical operators (`AND`, `OR`, `NOT`)
- [x] EXISTS operator (`EXIST(subquery)`)
- [x] IN operator (`search_value [NOT] IN value_set`)
- [x] IS operator
- [x] Concatenation operator `X || Y`
- [x] Function calls (SQL functions, UDFs, named arguments)
- [x] Aggregate function calls (`function_name([DISTINCT] args [...modifiers]) OVER over_clause`)
- [x] Window function calls (`function_name([argument_list]) OVER over_clause`)
- [x] Subqueries

#### Statements

##### Data Definition Language (DDL)

- Statements
    - [x] CREATE SCHEMA
    - [x] CREATE TABLE
    - [x] CREATE TABLE LIKE
    - [x] CREATE TABLE COPY
    - [x] CREATE SNAPSHOT TABLE
    - [x] CREATE TABLE CLONE
    - [x] CREATE VIEW
    - [ ] CREATE VIEW defined with column list with options (`CREATE VIEW t(field OPTIONS(...))`) - not supported by go-zetasql
    - [x] CREATE MATERIALIZED VIEW
    - [x] CREATE EXTERNAL TABLE
    - [x] CREATE EXTERNAL TABLE WITH CONNECTION
    - [x] CREATE FUNCTION
    - [x] CREATE TABLE FUNCTION
    - [x] CREATE PROCEDURE
    - [x] CREATE ROW ACCESS POLICY
    - [ ] CREATE CAPACITY - not supported by go-zetasql
    - [ ] CREATE RESERVATION - not supported by go-zetasql
    - [ ] CREATE ASSIGNMENT - not supported by go-zetasql
    - [ ] CREATE SEARCH INDEX - not supported by go-zetasql
    - [x] ALTER SCHEMA
    - [x] ALTER TABLE
    - [x] ALTER COLUMN
    - [x] ALTER VIEW
    - [x] ALTER MATERIALIZED VIEW
    - [ ] ALTER ORGANIZATION - not supported by go-zetaslq
    - [ ] ALTER PROJECT - not supported by go-zetaslq
    - [ ] ALTER BI_CAPACITY - not supported by go-zetaslq
    - [ ] ALTER CAPACITY - not supported by go-zetaslq
    - [x] DROP SCHEMA
    - [x] DROP TABLE
    - [x] DROP SNAPSHOT TABLE
    - [x] DROP EXTERNAL TABLE
    - [x] DROP VIEW
    - [x] DROP MATERIALIZED VIEW
    - [x] DROP FUNCTION
    - [x] DROP TABLE FUNCTION
    - [x] DROP PROCEDURE
    - [x] DROP ROW ACCESS POLICY
    - [ ] DROP CAPACITY - not supported by go-zetasql
    - [ ] DROP RESERVATION - not supported by go-zetasql
    - [ ] DROP ASSIGNMENT - not supported by go-zetasql
    - [x] DROP SEARCH INDEX

#### Data Manipulation Language (DML)

- [x] INSERT
- [x] DELETE
- [x] TRUNCATE TABLE
- [x] UPDATE
- [x] MERGE

#### Data Control Language (DCL)

- [ ] GRANT
- [ ] REVOKE

#### Procedural language

- [x] DECLARE
- [x] SET
- [ ] EXECUTE IMMEDIATE
- [ ] BEGIN...[EXCEPTION...]END
- [ ] CASE [search_expression]
- [x] IF
- [ ] Labels
- [ ] Loops
    - [ ] LOOP
    - [ ] REPEAT
    - [ ] WHILE
    - [ ] BREAK
    - [ ] LEAVE
    - [ ] CONTINUE
    - [ ] ITERATE
    - [ ] FOR...IN
- [ ] Transactions
    - [ ] BEGIN TRANSACTION
    - [ ] COMMIT TRANSACTION
    - [ ] ROLLBACK TRANSACTION
- [ ] RAISE
- [ ] RETURN
- [x] CALL

#### Export and load statements

- [ ] EXPORT DATA
- [ ] LOAD DATA

#### Debugging statements

- [ ] ASSERT

#### BigQuery ML SQL

- [ ] CREATE MODEL

#### Extensions

##### Jinja2

- [x] Template variable (`{{ variable }}`)
- [x] Template blocks
    - [x] For loop (`{% for expr in iterable %}...{% endfor %}`)
    - [x] If-endif statement (`{% if cond %}...{% endif %}`)
    - [x] If-else-endif statement (`{% if cond %}...{% else %}...{% endif %}`)
    - [x] If-elif-endif statement (`{% if cond %}...{% else %}...{% endif %}`)
    - [x] If-elif-else-endif statement (`{% if cond %}...{% elif ... %}...{% else %}...{% endif %}`)

Currently, templates should be replaceable by an identifier or query statement so that the resulting query is a valid ZetaSQL script.
If you follow this rule, you can use quite a lot of templates without losing the ability to format the SQL code before rendering.
