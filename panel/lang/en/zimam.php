<?php

// Zimam's own interface strings. The panel is English only in v1.0, and
// every user-facing string comes from these files (D3), so other languages
// can be added later without code changes.

return [
    'brand' => 'Zimam',

    'setup' => [
        'create' => [
            'title' => 'Set up Zimam',
            'heading' => 'Create the admin account',
            'subheading' => 'Choose the email and password you will sign in with. Two-factor authentication comes next.',
            'submit' => 'Create account',
        ],
        'reset' => [
            'title' => 'Reset access',
            'heading' => 'Reset access',
            'subheading' => 'This sets a new email and password, signs out every session, and asks for two-factor authentication again.',
            'submit' => 'Reset access',
        ],
        'email' => 'Email address',
        'password' => 'Password',
        'password_help' => 'At least 12 characters.',
        'password_confirmation' => 'Confirm password',
        'admin_name' => 'Admin',
        'admin_exists' => 'An admin account already exists. Reload this page to reset its access instead.',
    ],

    'confirm_password' => [
        'label' => 'Current password',
        'wrong' => 'The password is incorrect.',
    ],

    'agent' => [
        'unavailable' => [
            'title' => 'The agent didn’t answer',
            'body' => 'Nothing was changed on the server. Try again once the agent is back.',
        ],
        'banner' => [
            'heading' => 'The panel can’t reach the agent.',
            'since' => 'Its last answer came at :time.',
            'body' => 'Your sites keep running. Values shown are what the agent last reported, and actions are paused until it answers. Over SSH, check it with :command.',
            'command' => 'sudo systemctl status zimam-agent.socket',
        ],
        'paused' => 'Paused until the agent answers.',
    ],

    'jobs' => [
        'busy' => 'Gave up after 15 minutes: another job kept the server or this site busy.',
        'untracked' => 'The panel stopped checking this job, so its result is unknown. Check the server over SSH.',
    ],

    // Headline counts are spelled out up to twelve.
    'numbers' => ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten', 'eleven', 'twelve'],

    'dashboard' => [
        'headline' => [
            'unreachable' => 'Agent unreachable.',
            'none' => 'No warnings.',
            'warnings' => '{1} :number warning in force.|[2,*] :number warnings in force.',
            'warnings_lower' => '{1} :number warning|[2,*] :number warnings',
            'critical' => '{1} :number critical problem.|[2,*] :number critical problems.',
            'mixed' => ':critical critical, :warnings.',
        ],
        'lede' => [
            'none' => 'Nothing needs your attention.',
            'cached' => 'Last report at :time: :sentence',
            'no_report' => 'No report from the agent yet.',
        ],
        'issued' => 'Issued :issued · Next :next',
        'issued_at' => 'Issued :issued',
        'waiting' => 'Waiting for the agent',
        'checked' => 'checked :time',
        'warnings_label' => 'Warnings in force',
        'severity' => [
            'warning' => 'Warning',
            'critical' => 'Critical',
        ],
        'warnings' => [
            'reboot' => [
                'title' => 'Reboot required',
                'detail' => 'Installed updates take effect after a restart. Restart over SSH with sudo reboot when a short outage suits you.',
            ],
            'security_updates' => [
                'title' => '{1} :count security update pending|[2,*] :count security updates pending',
                'detail' => 'Install them over SSH with sudo apt upgrade.',
            ],
        ],
    ],

    'server' => [
        'heading' => 'Server',
        'as_of' => 'As last reported at :time.',
        'agent' => 'Agent',
        'agent_value' => 'zimam-agent :version, protocol :protocols',
        'nginx' => 'Nginx',
        'nginx_unknown' => 'Version unknown',
        'php' => 'PHP',
        'php_default' => ':version (default)',
        'php_panel' => ':version (panel)',
        'php_default_panel' => ':version (default, panel)',
        'php_none' => 'None installed',
        'panel_domain' => 'Panel domain',
        'acme' => 'Certificates',
        'acme_production' => 'Let’s Encrypt',
        'acme_staging' => 'Let’s Encrypt staging',
        'reboot' => 'Reboot',
        'reboot_required' => 'Required',
        'reboot_not_required' => 'Not required',
        'updates' => 'Updates',
        'updates_value' => ':total pending, :security for security, checked :time',
        'updates_none' => 'None pending, checked :time',
        'updates_unknown' => 'Not checked yet',
    ],
];
