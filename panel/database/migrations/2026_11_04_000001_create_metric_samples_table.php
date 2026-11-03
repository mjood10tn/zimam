<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * One system.metrics answer a minute (§7.5, §7.6). Counters are stored as
 * the agent reports them, cumulative since boot; rates come from two rows.
 * A week is about 10,000 rows, so nothing is downsampled.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('metric_samples', function (Blueprint $table) {
            $table->id();
            $table->timestamp('sampled_at')->index();
            $table->json('cpu');
            $table->double('load1');
            $table->double('load5');
            $table->double('load15');
            $table->unsignedBigInteger('mem_total');
            $table->unsignedBigInteger('mem_available');
            $table->unsignedBigInteger('swap_total');
            $table->unsignedBigInteger('swap_free');
            $table->json('disks');
            $table->unsignedBigInteger('net_rx');
            $table->unsignedBigInteger('net_tx');
            $table->json('services');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('metric_samples');
    }
};
