# Renders a Claude Code JSONL transcript (read with jq -R) as Markdown.
# Hidden system reminders, meta entries, and subagent sidechains are omitted.

def clean:
  gsub("(?s)<system-reminder>.*?</system-reminder>"; "")
  | gsub("^\\s+|\\s+$"; "");

def trunc($n):
  if length > $n then .[0:$n] + "\n… [truncated \(length - $n) chars]" else . end;

def fence: "~~~~\n" + . + "\n~~~~";

def result_text:
  if type == "string" then .
  elif type == "array" then map(select(.type == "text") | .text) | join("\n")
  else tostring end;

def tool_call:
  .name as $name
  | .input as $in
  | if $name == "Bash" then
      "**🔧 Bash**" + (if $in.description then " — \($in.description)" else "" end)
      + "\n\n" + ($in.command // "" | trunc(4000) | fence)
    elif ($in | type) == "object" and $in.file_path then
      "**🔧 \($name)** `\($in.file_path)`"
      + (if $name == "Write" or $name == "Edit" then
           "\n\n" + ($in | del(.file_path) | tojson | trunc(2000) | fence)
         else "" end)
    else
      "**🔧 \($name)**\n\n" + ($in | tojson | trunc(2000) | fence)
    end;

fromjson?
| select(.type == "user" or .type == "assistant")
| select((.isMeta // false) | not)
| select((.isSidechain // false) | not)
| .type as $role
| (.message.content // empty)
| if type == "string" then [{type: "text", text: .}] else . end
| .[]
| if .type == "text" then
    (.text | clean) as $t
    | select($t != "")
    | (if $role == "user" then "## 👤 User" else "## 🤖 Claude" end) + "\n\n" + $t
  elif .type == "tool_use" then
    tool_call
  elif .type == "tool_result" then
    "<details><summary>Tool result" + (if .is_error then " (error)" else "" end)
    + "</summary>\n\n" + (.content | result_text | clean | trunc(4000) | fence)
    + "\n\n</details>"
  else empty end
| . + "\n"
