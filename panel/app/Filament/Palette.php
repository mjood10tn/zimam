<?php

namespace App\Filament;

/**
 * DESIGN.md's colours as Filament palettes. Filament's primary colour also
 * drives navigation, focus, links and form controls, so primary is the navy
 * ink and emerald stays on primary action buttons alone (the Emerald Is the
 * Brand rule), which the theme sets from its own tokens.
 */
final class Palette
{
    /** Navy ink: 600 is ink on light grounds, 400 reads on dark ones. */
    public const INK = [
        50 => '#F8FAFC',
        100 => '#EEF2F7',
        200 => '#DCE3EC',
        300 => '#C3CFDE',
        400 => '#9DB0C7',
        500 => '#6B7F99',
        600 => '#0B2545',
        700 => '#0A2242',
        800 => '#091E3B',
        900 => '#081A33',
        950 => '#06132A',
    ];

    /** Cool slate: ground, track, rules, the three inks and the night sheets. */
    public const SLATE = [
        50 => '#F8FAFC',
        100 => '#E4E9F0',
        200 => '#DCE3EC',
        300 => '#B9C6D6',
        400 => '#9DB0C7',
        500 => '#56677E',
        600 => '#3B4F6B',
        700 => '#2C405F',
        800 => '#1F3657',
        900 => '#0F223D',
        950 => '#0A1A30',
    ];

    /** Amber with warn (#B45309) at 600 and warn-dark (#FBBF24) at 400. */
    public const AMBER = [
        50 => '#FFFBEB',
        100 => '#FEF3C7',
        200 => '#FDE68A',
        300 => '#FCD34D',
        400 => '#FBBF24',
        500 => '#F59E0B',
        600 => '#B45309',
        700 => '#92400E',
        800 => '#78350F',
        900 => '#5C2A0C',
        950 => '#451A03',
    ];

    /** Red with crit (#B91C1C) at 600 and crit-dark (#F87171) at 400. */
    public const RED = [
        50 => '#FEF2F2',
        100 => '#FEE2E2',
        200 => '#FECACA',
        300 => '#FCA5A5',
        400 => '#F87171',
        500 => '#EF4444',
        600 => '#B91C1C',
        700 => '#991B1B',
        800 => '#7F1D1D',
        900 => '#631717',
        950 => '#450A0A',
    ];
}
