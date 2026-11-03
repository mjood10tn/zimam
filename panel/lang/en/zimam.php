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
        'title' => 'Jobs',
        'label' => 'job',
        'empty' => 'No jobs yet.',
        'fields' => [
            'command' => 'Command',
            'subject' => 'Subject',
            'status' => 'Status',
            'related' => 'Site',
            'created_at' => 'Submitted',
            'started_at' => 'Started',
            'finished_at' => 'Finished',
            'agent_job_id' => 'Agent job id',
            'error' => 'Error',
        ],
        'status' => [
            'waiting' => 'Waiting',
            'queued' => 'Queued',
            'running' => 'Running',
            'succeeded' => 'Succeeded',
            'failed' => 'Failed',
            'cancelled' => 'Cancelled',
        ],
        'log' => [
            'heading' => 'Log',
            'empty' => 'Nothing logged yet.',
            'live' => 'Updating every 2 seconds.',
        ],
        'cancel' => [
            'label' => 'Cancel job',
            'heading' => 'Cancel this job?',
            'description' => 'The agent stops it where it is. What it already changed stays; running the same action again continues from there.',
            'done' => 'The job was cancelled',
        ],
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

    'sites' => [
        'title' => 'Sites',
        'label' => 'site',
        'empty' => 'No sites yet.',
        'cancelled' => 'Provisioning was cancelled before it finished.',
        'fields' => [
            'name' => 'Name',
            'name_help' => '2 to 24 lowercase letters, digits and hyphens, starting with a letter. It names the site’s folder and user, and can’t be changed.',
            'domain' => 'Domain',
            'domain_help' => 'Point its DNS at this server. International names are fine.',
            'type' => 'Type',
            'php_version' => 'PHP version',
            'php_option' => 'PHP :version',
            'php_default' => 'PHP :version (default)',
            'php_none' => 'No supported PHP version is installed. Install one under PHP versions.',
            'php_static' => 'None (static site)',
            'status' => 'Status',
            'url' => 'Address',
            'template_version' => 'Config template version',
            'last_error' => 'Last error',
        ],
        'types' => [
            'php' => 'PHP',
            'static' => 'Static',
        ],
        'status' => [
            'provisioning' => 'Provisioning',
            'active' => 'Active',
            'failed' => 'Failed',
            'deleting' => 'Deleting',
        ],
        'validation' => [
            'name' => 'Use 2 to 24 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen.',
            'reserved' => 'This name is reserved.',
            'name_taken' => 'Another site already has this name.',
            'domain' => 'Enter a domain such as shop.example.com: at least two labels, no wildcards.',
            'domain_taken' => 'Another site already uses this domain.',
        ],
        'tabs' => [
            'overview' => 'Overview',
            'jobs' => 'Jobs',
            'security' => 'Security',
            'logs' => 'Logs',
            'later' => 'Not available in this version yet.',
        ],
        'create' => [
            'submitted' => 'Provisioning :name',
        ],
        'retry' => [
            'label' => 'Retry',
            'submitted' => 'Provisioning :name again',
        ],
        'switch' => [
            'label' => 'Switch PHP version',
            'heading' => 'Switch :name to another PHP version',
            'description' => 'The new version starts serving before the old one stops, so the site stays up. Extensions and php.ini settings are per version.',
            'submit' => 'Switch',
            'none' => 'No other supported PHP version is installed. Install one under PHP versions.',
            'submitted' => 'Switching :name to PHP :version',
            'cancelled' => 'The PHP switch was cancelled before it finished. Switch again to finish it.',
        ],
        'delete' => [
            'label' => 'Delete',
            'heading' => 'Delete :name',
            'description' => 'This removes the site’s files, logs, certificate, PHP pool and user from the server. It can’t be undone.',
            'submitted' => 'Deleting :name',
            'done' => ':name is deleted',
            'cancelled' => 'Deleting was cancelled before it finished. Delete again to finish it.',
        ],
        'busy' => 'Wait for this site’s current job to finish.',
    ],

    'php' => [
        'title' => 'PHP versions',
        'empty' => 'No PHP versions reported by the agent yet.',
        'columns' => [
            'version' => 'Version',
            'roles' => 'Used by',
            'status' => 'Status',
            'eol' => 'Security support ends',
        ],
        'badges' => [
            'default' => 'Default',
            'panel' => 'Panel',
        ],
        'status' => [
            'installing' => 'Installing',
            'installed' => 'Installed',
            'uninstalling' => 'Uninstalling',
            'failed' => 'Failed',
        ],
        'submitted_body' => 'The job runs in the background. Follow it under Jobs.',
        'install' => [
            'label' => 'Install a version',
            'version' => 'Version',
            'option' => 'PHP :version (security support until :end)',
            'none' => 'Every supported version is already installed.',
            'submit' => 'Install',
            'submitted' => 'Installing PHP :version',
        ],
        'uninstall' => [
            'label' => 'Uninstall',
            'heading' => 'Uninstall PHP :version?',
            'description' => 'Its packages are removed from the server. Confirm with your password.',
            'submitted' => 'Uninstalling PHP :version',
            'blocked' => [
                'panel' => 'The panel runs on PHP :version.',
                'default' => 'PHP :version is the default. Make another version the default first.',
                'in_use' => 'Sites still run on PHP :version.',
                'busy' => 'A job for PHP :version hasn’t finished yet.',
                'not_installed' => 'PHP :version isn’t installed.',
            ],
        ],
        'set_default' => [
            'label' => 'Make default',
            'heading' => 'Make PHP :version the default?',
            'description' => 'The php command runs this version, and new sites start on it. Existing sites keep their version.',
            'submitted' => 'Making PHP :version the default',
        ],
    ],
];
