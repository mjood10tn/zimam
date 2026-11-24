<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Each "Install updates now" (R06-D13): its job, and once it ends what was
 * upgraded and restarted and whether a reboot is needed.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('os_update_runs', function (Blueprint $table) {
            $table->id();
            $table->foreignId('agent_job_id')->nullable()->constrained('agent_jobs')->cascadeOnDelete();
            $table->string('scope', 8);
            $table->string('status', 16);
            $table->json('upgraded')->nullable();
            $table->json('restarted')->nullable();
            $table->boolean('reboot_required')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('os_update_runs');
    }
};
