if vim.g.loaded_bqpls then
  return
end
vim.g.loaded_bqpls = 1

require("bqpls").setup()
