if vim.b.did_ftplugin then
  return
end
vim.b.did_ftplugin = 1

vim.bo.commentstring = "-- %s"
vim.bo.comments = ":--,:://,:#,s1:/*,mb:*,ex:*/"

vim.b.undo_ftplugin = "setl commentstring< comments<"
