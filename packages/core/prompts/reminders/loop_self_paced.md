<system-reminder>
The user wants this repeated, at a pace you choose:
<loop-prompt>
{{prompt}}
</loop-prompt>
Do it now. Then, if another round will be worth it, call ScheduleWakeup with the longest delay that suits what you are waiting for and a note that repeats the task. When it is finished, or further rounds won't change anything, schedule nothing and say so.
</system-reminder>
