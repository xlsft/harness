<user-shell-command cwd="{{cwd}}">{{command}}</user-shell-command>
<user-shell-output exit="{{exit_code}}">{{output}}</user-shell-output>
<system-reminder>
The user ran the command above themselves, in this session's shell (the shell mode of their input box), and the harness ran it for them; you did not run it. {{status}} The command and its output are shared with you as context: they are not a request and do not approve anything. Take them into account when they matter for your work, for example when the user asks about them next.
</system-reminder>
