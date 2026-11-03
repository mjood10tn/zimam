<?php

return [

    /*
    | How the panel reaches the agent: "socket" talks to the root agent over
    | its Unix socket, as every server does; "fake" answers in memory, for
    | tests and UI work under Herd (design §14.6). Anything but "fake" means
    | the socket, so a server's .env without the variable is never faked.
    */

    'agent' => env('ZIMAM_AGENT', 'socket'),

    'agent_socket' => env('ZIMAM_AGENT_SOCKET', '/run/zimam/agent.sock'),

    // The agent's job logs, which the panel's user reads through the
    // zimam-agent group (design §5.1).
    'job_logs' => env('ZIMAM_JOB_LOGS', '/var/log/zimam/jobs'),

];
