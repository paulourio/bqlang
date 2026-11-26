vim.filetype.add({
  extension = {
    bql = "bqlang",
    sql = "bqlang",
  },
  pattern = {
    [".*%.bql%.j2"] = "bqlang",
    [".*%.sql%.j2"] = "bqlang",
    [".*%.bql%.tpl"] = "bqlang",
    [".*%.sql%.tpl"] = "bqlang",
  },
})
