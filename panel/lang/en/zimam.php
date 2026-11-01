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
];
