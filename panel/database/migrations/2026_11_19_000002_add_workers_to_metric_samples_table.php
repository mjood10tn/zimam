<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * system.metrics' workers in each sample (v0.4.0), so the crash-loop
 * warning can compare a worker's restarts with ten minutes ago (R04-D17).
 * Older rows have none.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('metric_samples', function (Blueprint $table) {
            $table->json('workers')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('metric_samples', function (Blueprint $table) {
            $table->dropColumn('workers');
        });
    }
};
