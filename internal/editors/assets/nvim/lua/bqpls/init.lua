local M = {}

local stamped_bin_path = "__BQPLS_BIN_PATH__"

M._opts = {}
M._costs = {}

local function apply_highlights()
  local special = vim.api.nvim_get_hl(0, { name = "Special", link = false })
  local partitioned = {}
  if type(special) == "table" then
    for k, v in pairs(special) do
      if k ~= "default" then
        partitioned[k] = v
      end
    end
  end
  partitioned.bold = true
  partitioned.underline = true
  partitioned.default = true

  vim.api.nvim_set_hl(0, "@lsp.mod.partitioned.bqlang", partitioned)
  vim.api.nvim_set_hl(0, "@lsp.mod.clustered.bqlang", { italic = true, default = true })
end

local function refresh_codelens(bufnr)
  if type(M._opts) == "table" and M._opts.codelens == false then
    return
  end
  if not vim.lsp.codelens or type(vim.lsp.codelens.refresh) ~= "function" then
    return
  end
  vim.schedule(function()
    if vim.api.nvim_buf_is_valid(bufnr) and vim.api.nvim_buf_is_loaded(bufnr) then
      pcall(vim.lsp.codelens.refresh, { bufnr = bufnr })
    end
  end)
end

local function has_displayable_cost(result)
  if type(result) ~= "table" or not result.enabled then
    return false
  end
  if type(result.status) == "string" and result.status ~= "" then
    return true
  end
  return type(result.statements) == "table" and #result.statements > 0
end

local function handle_query_cost(_, result)
  if type(result) ~= "table" or type(result.uri) ~= "string" or result.uri == "" then
    return
  end

  local ok, bufnr = pcall(vim.uri_to_bufnr, result.uri)
  if not ok or not bufnr or not vim.api.nvim_buf_is_valid(bufnr) then
    return
  end

  if not has_displayable_cost(result) then
    M._costs[bufnr] = nil
  else
    M._costs[bufnr] = result
  end

  vim.schedule(function()
    pcall(vim.api.nvim_exec_autocmds, "User", {
      pattern = "BqplsQueryCostUpdated",
      data = { bufnr = bufnr, cost = M._costs[bufnr] },
    })
    pcall(vim.cmd.redrawstatus)
  end)

  refresh_codelens(bufnr)
end

local function resolve_cmd(opts)
  if type(opts) == "table" and type(opts.cmd) == "table" and #opts.cmd > 0 then
    return opts.cmd
  end
  if stamped_bin_path ~= "" and stamped_bin_path ~= ("__BQPLS_" .. "BIN_PATH__") then
    if vim.fn.executable(stamped_bin_path) == 1 then
      return { stamped_bin_path, "serve" }
    end
  end
  return { "bqpls", "serve" }
end

function M.query_cost(bufnr)
  bufnr = bufnr or vim.api.nvim_get_current_buf()
  return M._costs[bufnr]
end

function M.statusline(bufnr)
  bufnr = bufnr or vim.api.nvim_get_current_buf()
  local cost = M._costs[bufnr]
  if not has_displayable_cost(cost) then
    return ""
  end

  if cost.status == "error" or cost.status == "pending" or cost.status == "unavailable" then
    return cost.summary or ""
  end

  if type(cost.statements) == "table" and #cost.statements > 1 and bufnr == vim.api.nvim_get_current_buf() then
    local ok, cursor = pcall(vim.api.nvim_win_get_cursor, 0)
    if ok and type(cursor) == "table" then
      local line0 = (cursor[1] or 1) - 1
      for _, st in ipairs(cost.statements) do
        if type(st) == "table" and type(st.range) == "table" and type(st.range.start) == "table" and type(st.range["end"]) == "table" then
          if line0 >= (st.range.start.line or 0) and line0 <= (st.range["end"].line or 0) then
            return st.label or ""
          end
        end
      end
    end
  end

  return cost.summary or ""
end

function M.refresh_cost(bufnr)
  bufnr = bufnr or vim.api.nvim_get_current_buf()
  if not vim.api.nvim_buf_is_valid(bufnr) then
    return false
  end

  local clients = vim.lsp.get_clients({ bufnr = bufnr, name = "bqpls" })
  if type(clients) ~= "table" or #clients == 0 then
    return false
  end

  local uri = vim.uri_from_bufnr(bufnr)
  for _, client in ipairs(clients) do
    client:request("bqpls/refreshCost", {
      textDocument = { uri = uri },
    }, function(err, result)
      if not err and type(result) == "table" then
        handle_query_cost(nil, result)
      end
    end, bufnr)
  end

  return true
end

function M.attach(bufnr)
  bufnr = bufnr or vim.api.nvim_get_current_buf()
  if not vim.api.nvim_buf_is_valid(bufnr) then
    return nil
  end
  if vim.bo[bufnr].buftype ~= "" then
    return nil
  end

  local cmd = resolve_cmd(M._opts)
  if vim.fn.executable(cmd[1]) ~= 1 then
    return nil
  end

  local bufname = vim.api.nvim_buf_get_name(bufnr)
  local root_dir = vim.fs.root(bufnr, { ".bqfmt.toml", "dbt_project.yml", ".git" })
  if not root_dir or root_dir == "" then
    if bufname ~= "" then
      root_dir = vim.fs.dirname(bufname)
    else
      root_dir = vim.fn.getcwd()
    end
  end

  local init_options = nil
  if type(M._opts) == "table" then
    init_options = M._opts.init_options or M._opts.settings
  end

  return vim.lsp.start({
    name = "bqpls",
    cmd = cmd,
    root_dir = root_dir,
    init_options = init_options,
    handlers = {
      ["bqpls/queryCost"] = handle_query_cost,
    },
  }, {
    bufnr = bufnr,
  })
end

function M.setup(opts)
  M._opts = vim.tbl_deep_extend("force", {}, opts or {})
  apply_highlights()

  local group = vim.api.nvim_create_augroup("bqpls_lsp", { clear = true })
  vim.api.nvim_create_autocmd("ColorScheme", {
    group = group,
    callback = apply_highlights,
  })
  vim.api.nvim_create_autocmd("FileType", {
    group = group,
    pattern = "bqlang",
    callback = function(ev)
      M.attach(ev.buf)
    end,
  })
  vim.api.nvim_create_autocmd("LspAttach", {
    group = group,
    callback = function(ev)
      local client = vim.lsp.get_client_by_id(ev.data.client_id)
      if client and client.name == "bqpls" then
        refresh_codelens(ev.buf)
      end
    end,
  })
  vim.api.nvim_create_autocmd({ "BufDelete", "BufWipeout" }, {
    group = group,
    callback = function(ev)
      M._costs[ev.buf] = nil
    end,
  })

  if vim.fn.exists(":BqplsRefreshCost") ~= 2 then
    vim.api.nvim_create_user_command("BqplsRefreshCost", function()
      M.refresh_cost(vim.api.nvim_get_current_buf())
    end, { desc = "Refresh estimated BigQuery query cost via bqpls" })
  end

  for _, bufnr in ipairs(vim.api.nvim_list_bufs()) do
    if vim.api.nvim_buf_is_loaded(bufnr) and vim.bo[bufnr].filetype == "bqlang" then
      M.attach(bufnr)
    end
  end
end

return M
