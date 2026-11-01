<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/** One panel setting (§7.6): a key and its JSON value. PanelSettings reads them. */
class Setting extends Model
{
    protected $fillable = ['key', 'value'];

    protected function casts(): array
    {
        return ['value' => 'json'];
    }
}
